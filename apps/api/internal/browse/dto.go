package browse

import (
	"github.com/google/uuid"
)

// ConnectionDTO 连接安全 DTO（P0-06A §6，D02/D03 已批准）。
// 仅公开 id/name/engine/environment/database；host/port/secret_ref/secret_version/
// created_by/workspace_id/created_at/updated_at 一律不公开。
type ConnectionDTO struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Engine      string    `json:"engine"`
	Environment string    `json:"environment"`
	Database    string    `json:"database"`
}

// SchemaDTO Schema 浏览 DTO（P0-06A §7）。catalog 为连接的 database 名。
type SchemaDTO struct {
	Name    string `json:"name"`
	Catalog string `json:"catalog,omitempty"`
}

// TableDTO 表浏览 DTO（P0-06A §7）。type 为 "TABLE" / "VIEW"。
type TableDTO struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Type   string `json:"type"`
}

// ColumnDTO 列浏览 DTO（P0-06A §7）。
type ColumnDTO struct {
	Name       string `json:"name"`
	Ordinal    int    `json:"ordinal"`
	NativeType string `json:"native_type"`
	Nullable   bool   `json:"nullable"`
	HasDefault bool   `json:"has_default"`
}
