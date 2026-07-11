# The backend only: the cluster-in-a-box plus its control API. The dashboard is a static
# build that deploys separately (frontend/), and reaches this over CORS.
#
# Two stages, because the toolchain is ~800 MB and the thing we actually ship is one static
# binary. go.mod has zero dependencies, so there is nothing to vendor and no cache to warm.

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod ./
COPY cache/ cache/
COPY cluster/ cluster/
COPY logging/ logging/
COPY node/ node/
COPY ring/ ring/
COPY cmd/ cmd/

# CGO_ENABLED=0 is what makes the binary static, and static is what lets the final stage be
# scratch — with cgo on, the binary would want a libc that scratch does not have.
# -trimpath strips local paths out of the panic traces; -s -w drops the debug tables.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /server ./cmd/server

# scratch is genuinely empty: no shell, no libc, no CA certificates. That is fine here only
# because the nodes talk plain HTTP to each other over 127.0.0.1 and the process makes no
# outbound TLS calls. Add one and you need certificates — switch the base to
# gcr.io/distroless/static, which carries them.
FROM scratch
COPY --from=build /server /server

# $PORT is what the host injects and what defaultAddr() reads; 8080 is just the local
# default. EXPOSE documents it — it publishes nothing on its own.
ENV PORT=8080
EXPOSE 8080

# -log-file= disables the log FILE, not logging: the console handler still writes every
# record to stdout, which is exactly where a container platform collects logs from. Leaving
# it on would have the process trying to mkdir logs/ on a read-only filesystem.
ENTRYPOINT ["/server"]
CMD ["-log-file="]
