# Build the lab-cx proxy. CGO is required (tree-sitter), so the final image is
# glibc-based (distroless/base), not static.
FROM golang:1.25 AS build
WORKDIR /src
COPY . .
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=1 go build \
	-ldflags "-s -w -X github.com/kagenti/lab-context-engineering/internal/buildinfo.Version=${VERSION} -X github.com/kagenti/lab-context-engineering/internal/buildinfo.Commit=${COMMIT}" \
	-o /out/lab-cx ./cmd/proxy

FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /out/lab-cx /usr/local/bin/lab-cx
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/lab-cx"]
CMD ["proxy"]
