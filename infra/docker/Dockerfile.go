# Generic multi-stage Dockerfile for any Forge Go microservice.
# ARG SERVICE = gateway | orchestrator | figma-parser | codegen | sandbox | notifier
ARG SERVICE

FROM golang:1.22-alpine AS builder
ARG SERVICE
RUN apk add --no-cache git ca-certificates

WORKDIR /src
# Copy go.work and all modules
COPY go.work go.work.sum* ./
COPY shared/ ./shared/
COPY services/${SERVICE}/ ./services/${SERVICE}/

RUN cd services/${SERVICE} && go mod download
RUN cd services/${SERVICE} && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /svc ./main.go

FROM alpine:3.19
RUN apk add --no-cache ca-certificates curl
COPY --from=builder /svc /usr/local/bin/svc
ENTRYPOINT ["/usr/local/bin/svc"]
