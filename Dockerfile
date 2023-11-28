FROM golang:1.21.4-alpine3.17

RUN apk update && apk add git

COPY main.go /main.go
COPY entrypoint.sh /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
