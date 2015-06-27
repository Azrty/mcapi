package main

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/lukevers/mc/mcquery"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func updateQuery(serverAddr string) *ServerQuery {
	var online bool
	var veryOld bool
	var status *ServerQuery

	online = true
	veryOld = false

	resp, err := redisClient.Get("query:" + serverAddr).Result()
	if err != nil {
		status = &ServerQuery{}
	} else {
		json.Unmarshal([]byte(resp), &status)
	}

	status.Error = ""

	var conn *mcquery.Connection
	if online {
		conn, err = mcquery.Connect(serverAddr)
		if err != nil {
			isFatal := false
			errString := err.Error()
			for _, e := range fatalServerErrors {
				if strings.Contains(errString, e) {
					isFatal = true
				}
			}

			if isFatal {
				redisClient.SRem("serverquery", serverAddr)
				redisClient.Del("query:" + serverAddr)

				status.Status = "error"
				status.Error = "invalid hostname or port"
				status.Online = false

				return status
			}

			online = false
			status.Status = "success"
			status.Online = false
			status.LastUpdated = strconv.FormatInt(time.Now().Unix(), 10)
		}
	}

	redisClient.SAdd("serverquery", serverAddr)

	var query *mcquery.Stat
	if online {
		query, err = conn.FullStat()
		if err != nil {
			online = false
			status.Status = "success"
			status.Online = false
			status.LastUpdated = strconv.FormatInt(time.Now().Unix(), 10)
		}
	}

	if online {
		status.Status = "success"
		status.Online = true
		status.Motd = query.MOTD
		status.Version = query.Version
		status.GameType = query.GameType
		status.GameID = query.GameID
		status.ServerMod = query.ServerMod
		status.Map = query.Map
		status.Plugins = query.Plugins
		status.Players = ServerQueryPlayers{}
		status.Players.Max = query.MaxPlayers
		status.Players.Now = query.NumPlayers
		status.Players.List = query.Players
		status.LastUpdated = strconv.FormatInt(time.Now().Unix(), 10)
		status.LastOnline = strconv.FormatInt(time.Now().Unix(), 10)
	} else {
		i, err := strconv.ParseInt(status.LastOnline, 10, 64)
		if err != nil {
			i = time.Now().Unix()
		}

		if time.Unix(i, 0).Add(24 * time.Hour).Before(time.Now()) {
			veryOld = true
			log.Printf("Very old server %s in database\n", serverAddr)
		}
	}

	data, err := json.Marshal(status)
	if err != nil {
		status.Status = "error"
		status.Error = "internal server error (unable to jsonify server status)"
	}

	_, err = redisClient.Set("query:"+serverAddr, string(data), 6*time.Hour).Result()
	if err != nil {
		status.Status = "error"
		status.Error = "internal server error (unable to save json to redis)"
	}

	if veryOld || status.LastOnline == "" {
		redisClient.SRem("serverquery", serverAddr)
		redisClient.Del("query:" + serverAddr)
	}

	return status
}

func getServerQueryFromRedis(serverAddr string) *ServerQuery {
	resp, err := redisClient.Get("query:" + serverAddr).Result()
	if err != nil {
		status := updateQuery(serverAddr)

		return status
	}

	var status ServerQuery
	err = json.Unmarshal([]byte(resp), &status)
	if err != nil {
		return &ServerQuery{
			Status: "error",
			Error:  "internal server error (error loading json from redis)",
		}
	}

	return &status
}

func respondServerQuery(c *gin.Context) {
	c.Request.ParseForm()

	var serverAddr string

	ip := c.Request.Form.Get("ip")
	port := c.Request.Form.Get("port")

	if ip == "" {
		c.JSON(http.StatusBadRequest, &ServerQuery{
			Online: false,
			Status: "error",
			Error:  "missing data",
		})
		return
	}

	if port == "" {
		serverAddr = ip + ":25565"
	} else {
		serverAddr = ip + ":" + port
	}

	c.JSON(http.StatusOK, getServerQueryFromRedis(serverAddr))
}