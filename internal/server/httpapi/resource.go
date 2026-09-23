package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// maxBodyBytes 限制创建/更新请求体大小（输入校验，防无界读取）。
const maxBodyBytes = 1 << 20

// resourceObject 与存储层的泛型约束一致：T 为资源结构体，PT 为其指针类型。
type resourceObject[T any] interface {
	*T
	domain.ResourceObject
}

// resourceAPI 是同构资源集合的通用处理器：collection（GET/POST）+ item（GET/PUT/DELETE）。
type resourceAPI[T any, PT resourceObject[T]] struct {
	resource string
	repo     domain.Repository[PT]
	validate func(PT) error
	// onDelete 在资源删除成功后调用（machines：退役证书，KM-22）。
	onDelete func(ctx context.Context, name string) error
	// enrich 在读取与写入响应中补齐资源的派生字段。唯一使用者是
	// deployments：逐机推进状态存在 deployment_targets 表，而 §6.1 规定它以
	// status.targets 出现在 Deployment 资源里（FR-14.5 第 3 组的数据来源）。
	// 不改契约，只把已规定但此前未实现的字段填上（KM-25 契约缺口 #2）。
	enrich func(ctx context.Context, obj PT) error
}

func (h *resourceAPI[T, PT]) collection() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.list(w, r)
		case http.MethodPost:
			h.create(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, domain.ReasonMethodNotAllowed,
				"method "+r.Method+" not allowed on /"+h.resource, nil)
		}
	}
}

func (h *resourceAPI[T, PT]) item() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		switch r.Method {
		case http.MethodGet:
			obj, err := h.repo.Get(r.Context(), name)
			if err != nil {
				writeStoreError(w, err)
				return
			}
			if h.enrich != nil {
				if err := h.enrich(r.Context(), obj); err != nil {
					writeStoreError(w, err)
					return
				}
			}
			writeJSON(w, http.StatusOK, obj)
		case http.MethodPut:
			h.update(w, r, name)
		case http.MethodDelete:
			if err := h.repo.Delete(r.Context(), name); err != nil {
				writeStoreError(w, err)
				return
			}
			if h.onDelete != nil {
				if err := h.onDelete(r.Context(), name); err != nil {
					// 资源已删除；钩子失败只记录（如证书退役标记），
					// 后续 Connect 由"机器不存在"检查兜底拒绝。
					slog.Error("post-delete hook failed", "resource", h.resource, "name", name, "err", err)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			writeError(w, http.StatusMethodNotAllowed, domain.ReasonMethodNotAllowed,
				"method "+r.Method+" not allowed on /"+h.resource+"/{name}", nil)
		}
	}
}

func (h *resourceAPI[T, PT]) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.repo.List(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// §8.1：列表支持按 name 过滤（MVP 最小化）。
	if filter := r.URL.Query().Get("name"); filter != "" {
		filtered := items[:0]
		for _, it := range items {
			if it.Meta().Name == filter {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	if h.enrich != nil {
		for _, it := range items {
			if err := h.enrich(r.Context(), it); err != nil {
				writeStoreError(w, err)
				return
			}
		}
	}
	if items == nil {
		items = []PT{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *resourceAPI[T, PT]) create(w http.ResponseWriter, r *http.Request) {
	obj := PT(new(T))
	if !h.decode(w, r, obj) {
		return
	}
	// 服务端管理的元数据一律忽略请求携带的值。
	obj.Meta().UID = ""
	obj.Meta().ResourceVersion = 0
	obj.Meta().CreationTimestamp = time.Time{}
	// status 由控制器维护（§6.1），创建时初始化为空对象。
	obj.SetStatusJSON(json.RawMessage("{}"))
	if h.validate != nil {
		if err := h.validate(obj); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if err := h.repo.Create(r.Context(), obj); err != nil {
		writeStoreError(w, err)
		return
	}
	if h.enrich != nil {
		if err := h.enrich(r.Context(), obj); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, obj)
}

func (h *resourceAPI[T, PT]) update(w http.ResponseWriter, r *http.Request, name string) {
	obj := PT(new(T))
	if !h.decode(w, r, obj) {
		return
	}
	if obj.Meta().Name != name {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid,
			"metadata.name must match the resource name in the URL", nil)
		return
	}
	current, err := h.repo.Get(r.Context(), name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// 更新只作用 spec：元数据与 status 均以存储中的现值为准。
	obj.Meta().UID = current.Meta().UID
	obj.Meta().ResourceVersion = current.Meta().ResourceVersion
	obj.Meta().CreationTimestamp = current.Meta().CreationTimestamp
	obj.SetStatusJSON(current.StatusJSON())
	if h.validate != nil {
		if err := h.validate(obj); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if err := h.repo.Update(r.Context(), obj); err != nil {
		writeStoreError(w, err)
		return
	}
	if h.enrich != nil {
		if err := h.enrich(r.Context(), obj); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, obj)
}

func (h *resourceAPI[T, PT]) decode(w http.ResponseWriter, r *http.Request, obj PT) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(obj); err != nil {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid,
			"request body must be a JSON "+strings.TrimSuffix(h.resource, "s")+" object: "+err.Error(), nil)
		return false
	}
	return true
}
