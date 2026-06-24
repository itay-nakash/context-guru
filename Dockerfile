# Build the lab-cx proxy as a small static image.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod ./
# go.sum is added once external deps land; the COPY is tolerant of its absence.
COPY . .
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build \
	-ldflags "-s -w -X github.com/kagenti/lab-context-engineering/internal/buildinfo.Version=${VERSION} -X github.com/kagenti/lab-context-engineering/internal/buildinfo.Commit=${COMMIT}" \
	-o /out/lab-cx ./cmd/proxy

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/lab-cx /usr/local/bin/lab-cx
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/lab-cx"]
CMD ["proxy"]
