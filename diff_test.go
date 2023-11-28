package main

import (
	"github.com/charmbracelet/log"
	"github.com/stretchr/testify/assert"
	"strings"
	"testing"
)

func TestDiff(t *testing.T) {
	baseKustomizeCmd := newAppendableArgCmd("kustomize", "build", "test/base/")
	headKustomizeCmd := newAppendableArgCmd("kustomize", "build", "test/head/")
	sb := strings.Builder{}
	err := runCmd(*baseKustomizeCmd.Cmd, func(output string) {
		sb.WriteString(output)
		sb.WriteString("\n")
	})
	if err != nil {
		t.Fatalf("error running Kustomize on base: %v", err)
	}
	baseKustomization := sb.String()

	//run Kustomize
	sb.Reset()
	err = runCmd(*headKustomizeCmd.Cmd, func(output string) {
		sb.WriteString(output)
		sb.WriteString("\n")
	})
	if err != nil {
		t.Fatalf("error running Kustomize on head: %v", err)
	}
	headKustomization := sb.String()

	baseResult, err := parseManifests(baseKustomization)
	if err != nil {
		t.Fatalf("error parsing base manifests: %v", err)
	}
	headResult, err := parseManifests(headKustomization)
	if err != nil {
		t.Fatalf("error parsing head manifests: %v", err)
	}

	summary, err := buildSummary(baseResult, headResult)
	if err != nil {
		t.Fatalf("error building summary: %v", err)
	}

	assert.Equal(t, 3, len(summary.added))
	assert.Equal(t, 1, len(summary.removed))
	assert.Equal(t, 1, len(summary.modified))

	for _, a := range summary.added {
		log.Infof("added %s %s", a.Object.GetObjectKind().GroupVersionKind().Kind, a.Name)
	}

	for _, m := range summary.modified {
		log.Infof("modified %s %s", m.after.Object.GetObjectKind().GroupVersionKind().Kind, m.after.Name)
	}
	for _, r := range summary.removed {
		log.Infof("removed %s %s\n", r.Object.GetObjectKind().GroupVersionKind().Kind, r.Name)
	}

	log.Infof("added %d, modified %d, removed %d resources", len(summary.added), len(summary.modified), len(summary.removed))

}
