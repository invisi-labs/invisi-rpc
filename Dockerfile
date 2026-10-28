FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /protocol-rpc ./cmd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /protocol-rpc /protocol-rpc
ENV INVISI_RPC_DATA_DIR=/data
ENV INVISI_RPC_DEPLOYMENT_FILE=/deployment.json
EXPOSE 8080
ENTRYPOINT ["/protocol-rpc"]
CMD ["run"]
