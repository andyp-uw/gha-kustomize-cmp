package main

import (
	"bufio"
	"bytes"
	"fmt"
	v1apps "k8s.io/api/apps/v1"
	v1core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/scale/scheme/extensionsv1beta1"
	"os"
	"os/exec"
	"reflect"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/go-git/go-git/v5"
)

var (
	cmdGitLog     = exec.Command("git", "log", "--oneline", "--decorate=short")
	cmdGitClean   = exec.Command("git", "clean", "-xdf")
	cmdGitRestore = newAppendableArgCmd("git", "restore", "-SW", "--source")

	cmdRunKustomize = newAppendableArgCmd("kustomize", "build", "./")
)

func newAppendableArgCmd(name string, args ...string) *appendableArgCmd {
	cmd := exec.Command(name, args...)
	base := []string{name}
	return &appendableArgCmd{
		Cmd:     cmd,
		baseCmd: append(base, args...),
	}
}

type appendableArgCmd struct {
	*exec.Cmd
	baseCmd []string
}

func (c *appendableArgCmd) addArg(arg string) *appendableArgCmd {
	c.Args = append(c.Args, arg)
	return c
}

func (c *appendableArgCmd) reset() {
	c.Cmd = exec.Command(c.baseCmd[0], c.baseCmd[1:]...)
}

func main() {
	log.SetLevel(log.DebugLevel)
	baseRef := os.Getenv("GITHUB_BASE_REF")
	headRef := os.Getenv("GITHUB_HEAD_REF")

	if baseRef == "" || headRef == "" {
		log.Fatalf("GITHUB_BASE_REF and/or GITHUB_HEAD_REF are/is empty. Are you running this from a PR?")
	}

	log.Debugf("baseRef: %s, headRef: %s", baseRef, headRef)
	r, err := git.PlainOpen(".git")
	if err != nil {
		log.Fatalf("unable to open repository: %v", err)
	}

	log.Debug("git show-ref --head HEAD")
	ref, err := r.Head()
	if err != nil {
		log.Fatalf("unable to show-ref --head HEAD: %v", err)
	}
	headHash := ref.Hash().String()
	log.Infof("headHash: %s", headHash)

	baseHash := ""
	cmtToLkFor := fmt.Sprintf("(origin/%s)", baseRef)
	err = runCmd(*cmdGitLog, func(output string) {
		if strings.Contains(output, cmtToLkFor) {
			baseHash = output[:strings.Index(output, "(")-1]
		}
	})
	if err != nil {
		log.Fatalf("error running command: %s, %v", cmdGitLog.String(), err)
	}

	log.Infof("baseHash: %s", baseHash)

	//checkout baseHash
	err = cleanAndRestore(baseHash)
	if err != nil {
		log.Fatalf("error cleaning and restoring base (%s): %v", baseHash, err)
	}

	//run Kustomize
	sb := strings.Builder{}

	err = runCmd(*cmdRunKustomize.Cmd, func(output string) {
		sb.WriteString(output)
		sb.WriteString("\n")
	})
	if err != nil {
		log.Fatalf("error running Kustomize on base: %v", err)
	}
	baseKustomization := sb.String()

	//checkout headHash
	err = cleanAndRestore(headHash)
	if err != nil {
		log.Fatalf("error cleaning and restoring head (%s): %v", headHash, err)
	}

	//run Kustomize
	sb.Reset()
	err = runCmd(*cmdRunKustomize.Cmd, func(output string) {
		sb.WriteString(output)
		sb.WriteString("\n")
	})
	if err != nil {
		log.Fatalf("error running Kustomize on head: %v", err)
	}
	headKustomization := sb.String()

	baseManifests, err := parseManifests(baseKustomization)
	if err != nil {
		log.Fatalf("unable to parse manifests from base: %v", err)
	}
	headManifests, err := parseManifests(headKustomization)
	if err != nil {
		log.Fatalf("unable to parse manifests from head: %v", err)
	}
	summary, err := buildSummary(baseManifests, headManifests)
	if err != nil {
		log.Fatalf("unable to build summary: %v", err)
	}

	for _, a := range summary.added {
		log.Infof("added %s %s", a.Object.GetObjectKind().GroupVersionKind().Kind, a.Name)
	}

	log.Infof("added %d , modified %d, removed %d resources", len(summary.added), len(summary.modified), len(summary.removed))

}

// cleanAndRestore
func cleanAndRestore(hash string) error {
	err := runCmd(*cmdGitClean, func(output string) {
		log.Infof("clean: output: %s", output)
	})
	if err != nil {
		return fmt.Errorf("error running %s: %w", cmdGitClean.String(), err)
	}
	cmdGitRestore.reset()
	cmdGitRestore.addArg(hash)
	cmdGitRestore.addArg(".")
	err = runCmd(*cmdGitRestore.Cmd, func(output string) {
		log.Infof("restore: output: %s", output)
	})
	if err != nil {
		return fmt.Errorf("error running restore: %w", err)
	}
	return nil
}

// runCmd - for cmd, calls callback for each line of output of the cmd
func runCmd(cmd exec.Cmd, callback func(output string)) error {
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	scanner := bufio.NewScanner(stdout)

	done := make(chan struct{})

	go func() {
		i := 0
		for scanner.Scan() {
			i++
			output := scanner.Text()
			log.Debugf("cmd: %s, line %d: output: %s", cmd.String(), i, output)
			callback(output)
		}

		done <- struct{}{}
	}()

	log.Debugf("running %s", cmd.String())
	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("unable to run command '%s', %w", cmd.String(), err)
	}

	<-done

	return cmd.Wait()
}

func buildSummary(base *resource.Result, head *resource.Result) (*summary, error) {
	bI, err := base.Infos()
	if err != nil {
		return nil, fmt.Errorf("unable to get manifest info from base result: %w", err)
	}
	hI, err := head.Infos()
	if err != nil {
		return nil, fmt.Errorf("unable to get manifest info from head result: %w", err)
	}

	var removed []resource.Info
	var added []resource.Info
	var modified []modification
	for _, b := range bI {
		var j int
		found := false
		for j = range hI {
			if b.Name == hI[j].Name {
				found = true
				break
			}
		}

		if !found {
			removed = append(removed, *b)
		} else {
			if isModified(hI[j], b) {
				modified = append(modified, modification{before: *b, after: *hI[j]})
			}
		}
	}
	for _, h := range hI {
		found := false
		for j := range bI {
			if h.Name == bI[j].Name {
				found = true
				break
			}
		}

		if !found {
			added = append(added, *h)
		}
	}

	return &summary{
		removed:  removed,
		added:    added,
		modified: modified,
	}, nil

}

type summary struct {
	removed  []resource.Info
	added    []resource.Info
	modified []modification
}

func isModified(a, b *resource.Info) bool {
	return !reflect.DeepEqual(a, b)
}

type modification struct {
	before resource.Info
	after  resource.Info
}

func parseManifests(manifestContent string) (*resource.Result, error) {
	s := runtime.NewScheme()
	_ = v1apps.AddToScheme(s)
	_ = v1core.AddToScheme(s)
	_ = extensionsv1beta1.AddToScheme(s)
	// Create a local builder...
	builder := resource.NewLocalBuilder().
		WithScheme(s, scheme.Scheme.PrioritizedVersionsAllGroups()...).
		Stream(bytes.NewBufferString(manifestContent), "input").
		Flatten().
		ContinueOnError()

	// Run the builder
	result := builder.Do()

	if err := result.Err(); err != nil {
		fmt.Println("builder error:", err)
		return nil, err
	}
	return result, nil
}
