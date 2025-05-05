package internal

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/chdb-io/chdb-go/chdb"
	"github.com/labstack/echo/v4"
)

type SiteLogStorage struct {
	session *chdb.Session
}

func NewSiteLogStorage() (*SiteLogStorage, error) {
	session, err := chdb.NewSession("log.db")
	if err != nil {
		return nil, err
	}

	if _, err := session.Query(`CREATE DATABASE IF NOT EXISTS myrs`); err != nil {
		return nil, err
	}
	if _, err := session.Query(`CREATE TABLE IF NOT EXISTS myrs.logs(
	Time DateTime64,
	RemoteAddr String,
	Host String,
	User String,
	Status Int16,
	Protocol String,
	Method String,
	Path String,
	Request String,
	BodyBytesSent Int64,
	RequestTime Float64,
	UpstreamResponseTime Float64,
	UserAgent String,
	Referer String
)
ENGINE = MergeTree
ORDER BY (Time, Host)`); err != nil {
		return nil, err
	}

	return &SiteLogStorage{
		session: session,
	}, nil
}

func (s *SiteLogStorage) Close() error {
	s.session.Close()
	return nil
}

type FluentbitOutput struct {
	Date float64 `json:"date"`
	Log  string  `json:"log"`
}

func (o *FluentbitOutput) NginxLog() (*NginxLog, error) {
	var nginxLogRaw NginxLogRaw
	if err := json.Unmarshal([]byte(o.Log), &nginxLogRaw); err != nil {
		return nil, err
	}

	t, err := time.Parse("2006-01-02T15:04:05-07:00", nginxLogRaw.Time)
	if err != nil {
		return nil, err
	}
	var status int
	if nginxLogRaw.Status != "" {
		status, err = strconv.Atoi(nginxLogRaw.Status)
		if err != nil {
			return nil, err
		}
	}
	var bodyBytesSent int
	if nginxLogRaw.BodyBytesSent != "" {
		bodyBytesSent, err = strconv.Atoi(nginxLogRaw.BodyBytesSent)
		if err != nil {
			return nil, err
		}
	}
	var requestTime float64
	if nginxLogRaw.RequestTime != "" {
		requestTime, err = strconv.ParseFloat(nginxLogRaw.RequestTime, 64)
		if err != nil {
			return nil, err
		}
	}
	var upstreamResponseTime float64
	if nginxLogRaw.UpstreamResponseTime != "" {
		upstreamResponseTime, err = strconv.ParseFloat(nginxLogRaw.UpstreamResponseTime, 64)
		if err != nil {
			return nil, err
		}
	}
	nginxLog := NginxLog{
		Time:                 ClickHouseTime{t},
		RemoteAddr:           nginxLogRaw.RemoteAddr,
		Host:                 nginxLogRaw.Host,
		User:                 nginxLogRaw.User,
		Status:               status,
		Protocol:             nginxLogRaw.Protocol,
		Method:               nginxLogRaw.Method,
		Path:                 nginxLogRaw.Path,
		Request:              nginxLogRaw.Request,
		BodyBytesSent:        bodyBytesSent,
		RequestTime:          requestTime,
		UpstreamResponseTime: upstreamResponseTime,
		UserAgent:            nginxLogRaw.UserAgent,
		Referer:              nginxLogRaw.Referer,
	}
	return &nginxLog, nil
}

type NginxLogRaw struct {
	Time                 string `json:"time"`
	RemoteAddr           string `json:"remote_addr"`
	Host                 string `json:"host"`
	User                 string `json:"user"`
	Status               string `json:"status"`
	Protocol             string `json:"protocol"`
	Method               string `json:"method"`
	Path                 string `json:"path"`
	Request              string `json:"request"`
	BodyBytesSent        string `json:"body_bytes_sent"`
	RequestTime          string `json:"request_time"`
	UpstreamResponseTime string `json:"upstream_response_time"`
	UserAgent            string `json:"user_agent"`
	Referer              string `json:"referer"`
}

type ClickHouseTime struct {
	time.Time
}

func (t ClickHouseTime) String() string {
	return t.Format("2006-01-02 15:04:05")
}

func (t *ClickHouseTime) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}

	tt, err := time.Parse("2006-01-02 15:04:05", v)
	if err != nil {
		return err
	}

	t.Time = tt
	return nil
}

type NginxLog struct {
	Time                 ClickHouseTime
	RemoteAddr           string
	Host                 string
	User                 string
	Status               int
	Protocol             string
	Method               string
	Path                 string
	Request              string
	BodyBytesSent        int
	RequestTime          float64
	UpstreamResponseTime float64
	UserAgent            string
	Referer              string
}

func (s *SiteLogStorage) FluentbitHandler(c echo.Context) error {
	var req []*FluentbitOutput
	if err := c.Bind(&req); err != nil {
		return err
	}

	nginxLogs := []*NginxLog{}
	for _, l := range req {
		nginxLog, err := l.NginxLog()
		if err != nil {
			log.Println(err)
			continue
		}
		nginxLogs = append(nginxLogs, nginxLog)
	}

	for _, l := range nginxLogs {
		if _, err := s.session.Query(
			// chdbのインターフェース的にbindingできないのでfmt.Sprintfで整形しているが本当は良くない。
			fmt.Sprintf("INSERT INTO myrs.logs VALUES ('%s', '%s', '%s', '%s', %d, '%s', '%s', '%s', '%s', %d, %.3f, %.3f, '%s', '%s')",
				l.Time.Format("2006-01-02 15:04:05"), l.RemoteAddr, l.Host, l.User, l.Status, l.Protocol, l.Method, l.Path, l.Request, l.BodyBytesSent, l.RequestTime, l.UpstreamResponseTime, l.UserAgent, l.Referer,
			),
		); err != nil {
			return err
		}
	}

	return c.NoContent(http.StatusOK)
}

func (s *SiteLogStorage) GetLogs(host string) ([]*NginxLog, error) {
	res, err := s.session.Query(
		fmt.Sprintf("SELECT * FROM myrs.logs WHERE Host = '%s' ORDER BY Time DESC", host),
		"JSON",
	)
	if err != nil {
		return nil, err
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(res.Buf(), &result); err != nil {
		return nil, err
	}
	nginxLogs := []*NginxLog{}
	if err := json.Unmarshal(result["data"], &nginxLogs); err != nil {
		return nil, err
	}
	return nginxLogs, nil
}

type SummaryMinutelyRequestCountDataPoint struct {
	Time         ClickHouseTime
	RequestCount int
}

func (s *SiteLogStorage) GetMinutelyRequestCount(host string) ([]time.Time, []int, error) {
	until := time.Now().Truncate(time.Minute)
	from := until.Add(-time.Hour)
	layout := "2006-01-02 15:04:05"
	res, err := s.session.Query(
		fmt.Sprintf("SELECT toStartOfMinute(Time) as Time, COUNT(*) as RequestCount FROM myrs.logs WHERE Host = '%s' AND Time >= '%s' AND Time <= '%s' GROUP BY Time ORDER BY Time ASC",
			host,
			from.Format(layout),
			until.Format(layout),
		),
		"JSON",
	)
	if err != nil {
		return nil, nil, err
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(res.Buf(), &result); err != nil {
		return nil, nil, err
	}
	dataPoints := []*SummaryMinutelyRequestCountDataPoint{}
	if err := json.Unmarshal(result["data"], &dataPoints); err != nil {
		return nil, nil, err
	}

	times := []time.Time{}
	requestCounts := []int{}
	i := 0
	for t := from; t.Compare(until) <= 0; t = t.Add(time.Minute) {
		times = append(times, t)
		var requestCount int
		if i < len(dataPoints) && t.Equal(dataPoints[i].Time.Time) {
			requestCount = dataPoints[i].RequestCount
			i++
		}
		requestCounts = append(requestCounts, requestCount)
	}
	return times, requestCounts, nil
}
