# The backend only: the cluster-in-a-box plus its control API. The dashboard is a static
# build that deploys separately (frontend/) and reaches this over CORS.
#
# Two stages: the toolchain is ~800 MB and what we ship is one static binary.

FROM golang:1.26 AS build
WORKDIR /src

# ⚠️ Copy the tree; do NOT list the packages. This was one COPY line per package until adding
# notify/ without its line broke the deploy — an enumerated source tree is a second source of
# truth, and production is the only place that checks it. .dockerignore says what to leave out.
COPY . .

# CGO_ENABLED=0 is what makes the binary static, and static is what lets the final stage be
# scratch — with cgo on, the binary would want a libc that scratch does not have.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /server ./cmd/server

# scratch is genuinely empty: no shell, no libc, no CA certificates.
#
# ⚠️ Trusting a TLS certificate means checking who signed it against a list of trusted
# signers, and that list is just a file. The nodes talk plain HTTP over 127.0.0.1, but
# notify/ POSTs to https://ntfy.sh — without the bundle every push dies "x509: certificate
# signed by unknown authority" while the deploy goes green and the health check passes.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /server /server

# $PORT is what the host injects and what defaultAddr() reads; EXPOSE publishes nothing.
ENV PORT=8080
EXPOSE 8080

# -log-file= disables the log FILE, not logging: the console handler still writes to stdout,
# which is where a container platform collects logs. Leaving it on would have the process
# trying to mkdir logs/ on a read-only filesystem.
ENTRYPOINT ["/server"]
CMD ["-log-file="]
