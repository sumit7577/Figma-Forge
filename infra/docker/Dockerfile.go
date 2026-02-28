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
# Copy all service directories (needed because go.work references all of them)
COPY services/ ./services/

RUN cd services/${SERVICE} && go mod download
RUN cd services/${SERVICE} && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /svc .

FROM alpine:3.19
# Copy SSL certificates from builder to avoid network issues during docker build
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /svc /usr/local/bin/svc
ENTRYPOINT ["/usr/local/bin/svc"]
