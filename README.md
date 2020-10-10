MCApi
=====

An API for fetching the status of and querying Minecraft servers.

It is running at [mcapi.us](https://mcapi.us).

## Configuration

```bash
go get -u github.com/go-bindata/go-bindata/...
go generate
go build

export MCAPI_HTTPAPPHOST=127.0.0.1:8080 MCAPI_REDISHOST=127.0.0.1:6379
./mcapi
```

| Env Variable        | Description                                    |
| ------------------- | ---------------------------------------------- |
| `MCAPI_HTTPAPPHOST` | Host and port for the HTTP server to listen on |
| `MCAPI_REDISHOST`   | Host and port of Redis server                  |
| `MCAPI_SENTRYDSN`   | Optional Sentry DSN for error reporting        |
| `MCAPI_ADMINKEY`    | Secret token for authenticated operations      |

Rate limiting with Cloudflare requires setting the following environment variables:
* `CLOUDFLARE_EMAIL` &mdash; your Cloudflare account email address
* `CLOUDFLARE_AUTH` &mdash; your Cloudflare authentication token

Disable ratelimiting generally or with Cloudflare by using built-time variables
to update `rateLimitEnabled` and `cloudflareEnabled` or modifying the code
in `ratelimit.go#16-17`.

Setting `APPROVED_IPS` to a comma separated list of IP addresses will prevent
the rate limits from applying to those addresses.
