package api

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/xiaoguan521/cacheos/internal/console/alerts"
	"github.com/xiaoguan521/cacheos/internal/console/store"
)

// 告警（规格书 7.1 M9）。

func (s *Server) alertsGet(c *gin.Context) {
	rules, err := s.Alerts.Rules()
	if err != nil {
		failErr(c, err)
		return
	}
	q := s.q()
	chs, err := q.AlertChannel.WithContext(c.Request.Context()).Order(q.AlertChannel.ID).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	type chView struct {
		*store.AlertChannel
		HasSecret bool `json:"has_secret"`
	}
	views := []chView{}
	for _, ch := range chs {
		views = append(views, chView{ch, ch.SecretEnc != ""})
	}
	evs, err := q.AlertEvent.WithContext(c.Request.Context()).Order(q.AlertEvent.ID.Desc()).Limit(100).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	if evs == nil {
		evs = []*store.AlertEvent{}
	}
	c.JSON(http.StatusOK, gin.H{"rules": rules, "channels": views, "events": evs, "kind_names": alerts.KindName,
		"im_licensed": s.Alerts.IMAllow == nil || s.Alerts.IMAllow()})
}

func (s *Server) saveChannel(c *gin.Context) {
	var id uint
	if c.Param("id") != "" {
		v, ok := parseID(c)
		if !ok {
			return
		}
		id = v
	}
	var in alerts.ChannelInput
	if err := c.ShouldBindJSON(&in); err != nil {
		failErr(c, err)
		return
	}
	ch, err := s.Alerts.SaveChannel(id, in, c.GetString("user"))
	if err != nil {
		if err == alerts.ErrIMNotLicensed {
			fail(c, http.StatusPaymentRequired, "LICENSE_FEATURE_DENIED", err.Error())
			return
		}
		failErr(c, err)
		return
	}
	s.audit(c, "alert.channel_save", "alert_channel", fmt.Sprint(ch.ID), map[string]any{"name": ch.Name, "type": ch.Type, "secret_changed": in.Secret != ""})
	c.JSON(http.StatusOK, ch)
}

func (s *Server) deleteChannel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	q := s.q()
	if _, err := q.AlertChannel.WithContext(c.Request.Context()).Where(q.AlertChannel.ID.Eq(id)).Delete(); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "alert.channel_delete", "alert_channel", fmt.Sprint(id), nil)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) testChannel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	q := s.q()
	ch, err := q.AlertChannel.WithContext(c.Request.Context()).Where(q.AlertChannel.ID.Eq(id)).First()
	if err != nil {
		failErr(c, store.ErrNotFound)
		return
	}
	if err := s.Alerts.Send(ch, alerts.Message{Title: "【测试】EdgeTraffic OS 告警通道", Text: "这是一条测试消息，由 " + c.GetString("user") + " 发送。"}); err != nil {
		fail(c, http.StatusBadGateway, "SEND_FAILED", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) saveRule(c *gin.Context) {
	var req struct {
		Enabled    bool    `json:"enabled"`
		Threshold  float64 `json:"threshold"`
		ChannelIDs []uint  `json:"channel_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failErr(c, err)
		return
	}
	kind := c.Param("kind")
	if err := s.Alerts.SaveRule(kind, req.Enabled, req.Threshold, req.ChannelIDs, c.GetString("user")); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "alert.rule_save", "alert_rule", kind, req)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
