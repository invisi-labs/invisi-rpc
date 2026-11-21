# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /invisi-rpc ./cmd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /invisi-rpc /invisi-rpc
COPY LICENSE /licenses/invisi-rpc/LICENSE
ENV INVISI_RPC_DATA_DIR=/data
ENV INVISI_RPC_DEPLOYMENT_FILE=/deployment.json
EXPOSE 8080
ENTRYPOINT ["/invisi-rpc"]
CMD ["run"]
