# The backend only: the cluster-in-a-box plus its control API. The dashboard is a static
# build that deploys separately (frontend/), and reaches this over CORS.
#
# Two stages, because the toolchain is ~800 MB and the thing we actually ship is one static
# binary. go.mod has zero dependencies, so there is nothing to vendor and no cache to warm.

FROM golang:1.26 AS build
WORKDIR /src

# ⚠️ Copy the tree, do NOT list the packages. This used to be one COPY line per package,
# which is a second source of truth about what packages exist — and the only place it gets
# checked is production. Adding notify/ and forgetting its line broke the deploy with
# "no required module provides package .../notify", while the build stayed green locally,
# where the directory is simply there. .dockerignore says what to leave out.
COPY . .

# CGO_ENABLED=0 is what makes the binary static, and static is what lets the final stage be
# scratch — with cgo on, the binary would want a libc that scratch does not have.
# -trimpath strips local paths out of the panic traces; -s -w drops the debug tables.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /server ./cmd/server

# scratch is genuinely empty: no shell, no libc, no CA certificates.
#
# The nodes talk plain HTTP to each other over 127.0.0.1, so for a long time that was fine.
# It stopped being fine the moment notify/ started POSTing to https://ntfy.sh: verifying a
# TLS certificate means checking who signed it, and the list of signers you trust is just a
# file on disk. Without it every push dies with "x509: certificate signed by unknown
# authority" — the deploy still goes green, the health check still passes, and the feature
# is simply, silently dead.
#
# So bring the file. It is the same bundle the (Debian-based) build stage already carries,
# and Go looks for it here by default.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
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
