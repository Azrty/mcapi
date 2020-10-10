package main

import (
	"errors"
	"flag"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/OneOfOne/cmap/stringcmap"
	"github.com/Syfaro/mcapi/types"
	"github.com/getsentry/raven-go"
	"github.com/gin-contrib/sentry"
	"github.com/gin-gonic/gin"
	"github.com/gocraft/work"
	"github.com/gomodule/redigo/redis"
	"github.com/kelseyhightower/envconfig"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

//go:generate go-bindata -o bindata.go templates/ files/ scripts/

// Config is configuration data to run.
type Config struct {
	HTTPAppHost string
	RedisHost   string
	SentryDSN   string
	AdminKey    string
}

var redisPool *redis.Pool

var enqueuer *work.Enqueuer

var pingMap *stringcmap.CMap
var queryMap *stringcmap.CMap

var fatalServerErrors = []string{
	"no such host",
	"no route",
	"unknown port",
	"too many colons in address",
	"invalid argument",
}

var (
	totalRequests = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcapi_http_requests_total",
		Help: "The total number of HTTP requests",
	})

	serverStatusCacheMiss = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcapi_status_cache_miss_total",
		Help: "The total number of cache misses for server status",
	})

	serverStatusCacheHit = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcapi_status_cache_hit_total",
		Help: "The total number of cache hits for server status",
	})

	serverQueryCacheMiss = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcapi_query_cache_miss_total",
		Help: "The total number of cache misses for server query",
	})

	serverQueryCacheHit = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcapi_query_cache_hit_total",
		Help: "The total number of cache hits for server query",
	})
)

func updateServers() {
	pingMap.ForEachLocked(func(key string, _ interface{}) bool {
		enqueuer.Enqueue("status", work.Q{"serverAddr": key})

		return true
	})

	queryMap.ForEachLocked(func(key string, _ interface{}) bool {
		enqueuer.Enqueue("query", work.Q{"serverAddr": key})

		return true
	})
}

// JobCtx is context for a running job.
type JobCtx struct{}

func jobMiddleware(job *work.Job, next work.NextMiddlewareFunc) error {
	log.Printf("Running %s: %+v\n", job.Name, job.Args)
	return next()
}

func jobUpdate(job *work.Job) error {
	e := make(chan error, 1)

	go func() {
		if _, ok := job.Args["serverAddr"]; ok {
			serverAddr := job.ArgString("serverAddr")

			if job.Name == "query" {
				res := updateQuery(serverAddr)

				if res.Error != "" {
					e <- errors.New(res.Error)
				} else {
					e <- nil
				}
			} else if job.Name == "status" {
				res := updatePing(serverAddr)

				if res.Error != "" {
					e <- errors.New(res.Error)
				} else {
					e <- nil
				}
			}
		} else {
			e <- errors.New("missing server address")
		}
	}()

	select {
	case res := <-e:
		return res
	case <-time.After(5 * time.Second):
		return errors.New("job took longer than 5 seconds")
	}
}

func main() {
	fetch := flag.Bool("fetch", true, "enable fetching server data")

	flag.Parse()

	log.SetOutput(os.Stdout)

	var cfg Config
	err := envconfig.Process("mcapi", &cfg)
	if err != nil {
		panic(err)
	}

	raven.SetDSN(cfg.SentryDSN)

	pingMap = stringcmap.New()
	queryMap = stringcmap.New()

	redisPool = &redis.Pool{
		MaxActive:   200,
		MaxIdle:     100,
		Wait:        true,
		IdleTimeout: 60 * time.Second,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", cfg.RedisHost)
		},
	}

	if *fetch {
		log.Println("Fetching enabled.")

		enqueuer = work.NewEnqueuer("mcapi", redisPool)

		pool := work.NewWorkerPool(JobCtx{}, 50, "mcapi", redisPool)

		pool.Middleware(jobMiddleware)

		pool.Job("query", jobUpdate)
		pool.Job("status", jobUpdate)

		go pool.Start()

		updateServers()
		go func() {
			for range time.Tick(time.Minute * 5) {
				updateServers()
			}
		}()
	} else {
		log.Println("Fetching is NOT enabled.")
	}

	router := gin.New()
	router.Use(sentry.Recovery(raven.DefaultClient, false))

	template, err := template.New("index.html").Parse(string(MustAsset("templates/index.html")))
	if err != nil {
		panic(err)
	}
	router.SetHTMLTemplate(template)

	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	router.GET("/scripts/*filepath", func(c *gin.Context) {
		p := c.Param("filepath")
		data, err := Asset("scripts" + p)
		if err != nil {
			c.AbortWithStatus(404)
			return
		}
		c.Writer.Write(data)
	})

	router.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET")
		c.Writer.Header().Set("Cache-Control", "max-age=300, public, s-maxage=300")

		r := redisPool.Get()
		r.Do("INCR", "mcapi")
		r.Close()

		totalRequests.Inc()
	})

	router.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", gin.H{})
	})

	router.GET("/health", func(c *gin.Context) {
		c.String(http.StatusOK, ":3")
	})

	router.GET("/stats", func(c *gin.Context) {
		r := redisPool.Get()
		stats, err := redis.Int64(r.Do("GET", "mcapi"))
		r.Close()

		if err != nil {
			raven.CaptureErrorAndWait(err, nil)
		}

		c.JSON(http.StatusOK, gin.H{
			"stats": stats,
			"time":  time.Now().UnixNano(),
		})
	})

	router.GET("/server/status", respondServerStatus)
	router.GET("/minecraft/1.3/server/status", respondServerStatus)

	router.GET("/server/image", respondServerImage)

	router.GET("/server/query", respondServerQuery)
	router.GET("/minecraft/1.3/server/query", respondServerQuery)

	authorized := router.Group("/admin", gin.BasicAuth(gin.Accounts{
		"mcapi": cfg.AdminKey,
	}))

	authorized.GET("/ping", func(c *gin.Context) {
		items := strings.Builder{}

		pingMap.ForEachLocked(func(key string, val interface{}) bool {
			ping, ok := val.(*types.ServerStatus)
			if !ok {
				return true
			}

			items.WriteString(key)
			items.Write([]byte(" - "))
			items.WriteString(ping.LastUpdated)
			items.Write([]byte("\n"))

			return true
		})

		c.String(http.StatusOK, items.String())
	})

	authorized.GET("/query", func(c *gin.Context) {
		items := strings.Builder{}

		queryMap.ForEachLocked(func(key string, val interface{}) bool {
			ping, ok := val.(*types.ServerQuery)
			if !ok {
				return true
			}

			items.WriteString(key)
			items.Write([]byte(" - "))
			items.WriteString(ping.LastUpdated)
			items.Write([]byte("\n"))

			return true
		})

		c.String(http.StatusOK, items.String())
	})

	authorized.POST("/clear", func(c *gin.Context) {
		pingMap.ForEach(func(key string, _ interface{}) bool {
			pingMap.Delete(key)
			return true
		})

		queryMap.ForEach(func(key string, _ interface{}) bool {
			queryMap.Delete(key)
			return true
		})

		c.String(http.StatusOK, "Cleared items.")
	})

	router.Run(cfg.HTTPAppHost)
}
