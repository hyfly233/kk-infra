package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"kk-infra/lib/middleware"
	"kk-infra/services/pipeline"
	"kk-infra/services/pipeline/internal/data"
)

type Service struct {
	store                                  data.Store
	modelRegistry, probeURL, pipelineToken string
	http                                   *http.Client
	audit                                  data.AuditStore
}

func New(store data.Store, modelRegistry, probeURL, token string, audits ...data.AuditStore) *Service {
	var audit data.AuditStore
	if len(audits) > 0 {
		audit = audits[0]
	}
	return &Service{store: store, modelRegistry: modelRegistry, probeURL: probeURL, pipelineToken: token, http: &http.Client{Timeout: 30 * time.Second}, audit: audit}
}

func id() string { b := make([]byte, 10); _, _ = rand.Read(b); return "rel-" + hex.EncodeToString(b) }

func (s *Service) Start(ctx context.Context, versionID, operator string) (*pipeline.ReleaseRecord, error) {
	if versionID == "" || operator == "" {
		return nil, fmt.Errorf("modelVersionId 和 operator 必填")
	}
	now := time.Now().UTC()
	r := &pipeline.ReleaseRecord{ID: id(), ModelVersionID: versionID, Operator: operator, Status: "RUNNING", CreatedAt: now, UpdatedAt: now}
	if err := s.store.Create(r); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "release.start", operator, r.ID, "启动模型版本 "+versionID+" 的发布流水线")
	if err := s.modelAction(ctx, versionID, "validate", false); err != nil {
		return s.fail(ctx, r, pipeline.StageArtifactValidate, err)
	}
	r.StageResults = append(r.StageResults, pipeline.RunResult{Stage: pipeline.StageArtifactValidate, Status: "passed", Message: "S3 artifact 校验通过"})
	started := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.probeURL+"/health", nil)
	resp, err := s.http.Do(req)
	if err != nil || resp.StatusCode/100 != 2 {
		if resp != nil {
			resp.Body.Close()
		}
		if err == nil {
			err = fmt.Errorf("探针返回 %s", resp.Status)
		}
		return s.fail(ctx, r, pipeline.StageProbe, err)
	}
	resp.Body.Close()
	r.StageResults = append(r.StageResults, pipeline.RunResult{Stage: pipeline.StageProbe, Status: "passed", Message: "临时探针健康检查通过", DurationMs: time.Since(started).Milliseconds()})
	bench, err := s.benchmark(ctx, versionID)
	if err != nil {
		return s.fail(ctx, r, pipeline.StageBenchmark, err)
	}
	r.Benchmark = bench
	r.StageResults = append(r.StageResults, pipeline.RunResult{Stage: pipeline.StageBenchmark, Status: "passed", Message: "基准测试通过"}, pipeline.RunResult{Stage: pipeline.StageApproval, Status: "pending", Message: "等待人工审批"})
	r.Status = "PENDING_APPROVAL"
	r.UpdatedAt = time.Now().UTC()
	if err := s.store.Update(r); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Service) Approve(ctx context.Context, id, approver, message string, approved bool) (*pipeline.ReleaseRecord, error) {
	r, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	if r.Status != "PENDING_APPROVAL" {
		return nil, fmt.Errorf("当前状态 %s 不允许审批", r.Status)
	}
	r.ApprovedBy = approver
	r.ApprovalMessage = message
	r.UpdatedAt = time.Now().UTC()
	idx := len(r.StageResults) - 1
	if !approved {
		r.Status = "REJECTED"
		r.StageResults[idx].Status = "failed"
		r.StageResults[idx].Message = "人工拒绝: " + message
		_ = s.store.Update(r)
		s.recordAudit(ctx, "release.reject", approver, r.ID, "拒绝模型版本 "+r.ModelVersionID+" 的发布: "+message)
		return r, nil
	}
	r.StageResults[idx].Status = "passed"
	r.StageResults[idx].Message = "人工审批通过"
	if err := s.modelAction(ctx, r.ModelVersionID, "release", true); err != nil {
		return s.fail(ctx, r, pipeline.StageRelease, err)
	}
	now := time.Now().UTC()
	r.Status = "RELEASED"
	r.ReleasedAt = &now
	r.UpdatedAt = now
	r.StageResults = append(r.StageResults, pipeline.RunResult{Stage: pipeline.StageRelease, Status: "passed", Message: "模型版本已发布"})
	if err := s.store.Update(r); err != nil {
		return nil, err
	}
	s.recordAudit(ctx, "release.approve", approver, r.ID, "批准并发布模型版本 "+r.ModelVersionID)
	return r, nil
}
func (s *Service) Get(id string) (*pipeline.ReleaseRecord, error) { return s.store.Get(id) }
func (s *Service) fail(ctx context.Context, r *pipeline.ReleaseRecord, stage pipeline.Stage, cause error) (*pipeline.ReleaseRecord, error) {
	r.Status = "FAILED"
	r.UpdatedAt = time.Now().UTC()
	r.StageResults = append(r.StageResults, pipeline.RunResult{Stage: stage, Status: "failed", Message: cause.Error()})
	_ = s.store.Update(r)
	s.recordAudit(ctx, "release.failed", r.Operator, r.ID, string(stage)+": "+cause.Error())
	return r, cause
}

func (s *Service) recordAudit(ctx context.Context, action, actor, resource, detail string) {
	if s.audit == nil {
		return
	}
	entry := data.AuditEntry{Action: action, Actor: actor, Resource: resource, RequestID: middleware.GetRequestID(ctx), Detail: detail, CreatedAt: time.Now().UTC()}
	if err := s.audit.Write(entry); err != nil {
		slog.Default().Warn("发布流水线审计写入失败", "action", action, "resource", resource, "err", err)
	}
}
func (s *Service) modelAction(ctx context.Context, versionID, action string, token bool) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.modelRegistry+"/api/v1/versions/"+versionID+"/"+action, nil)
	if token {
		req.Header.Set("X-Pipeline-Token", s.pipelineToken)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("modelregistry %s 返回 %s", action, resp.Status)
	}
	return nil
}
func (s *Service) benchmark(ctx context.Context, versionID string) (*pipeline.BenchmarkResult, error) {
	body, _ := json.Marshal(map[string]any{"model": "pipeline-probe", "messages": []map[string]string{{"role": "user", "content": "ping"}}})
	started := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.probeURL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("benchmark 返回 %s", resp.Status)
	}
	latency := float64(time.Since(started).Milliseconds())
	return &pipeline.BenchmarkResult{ModelVersionID: versionID, TTFTMs: latency, TokensPerSec: 1000 / max(latency, 1), Requests: 1}, nil
}
