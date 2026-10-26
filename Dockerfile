FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /protocol-rpc ./cmd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /protocol-rpc /protocol-rpc
COPY --from=build /src/deployment.json /deployment.json
ENV INVISI_RPC_DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/protocol-rpc"]
CMD ["run"]
