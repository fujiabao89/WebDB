package adapter

type TableType string

const (
	TableTypeTable TableType = "TABLE"
	TableTypeView  TableType = "VIEW"
)

type Schema struct {
	Name    string `json:"name"`
	Catalog string `json:"catalog,omitempty"`
}

type Table struct {
	Schema string    `json:"schema"`
	Name   string    `json:"name"`
	Type   TableType `json:"type"`
}

type Column struct {
	Name       string `json:"name"`
	Ordinal    int    `json:"ordinal"`
	NativeType string `json:"native_type"`
	Nullable   bool   `json:"nullable"`
	HasDefault bool   `json:"has_default"`
}

type PrimaryKey struct {
	ColumnNames []string `json:"column_names"`
}

type ForeignKey struct {
	ConstraintName string   `json:"constraint_name"`
	ColumnNames    []string `json:"column_names"`
	RefSchema      string   `json:"ref_schema"`
	RefTable       string   `json:"ref_table"`
	RefColumnNames []string `json:"ref_column_names"`
}

type Index struct {
	Name              string        `json:"name"`
	Unique            bool          `json:"unique"`
	Method            string        `json:"method"`
	Columns           []IndexColumn `json:"columns"`
	Predicate         *string       `json:"predicate,omitempty"`
	Complete          bool          `json:"complete"`
	UnsupportedReason *string       `json:"unsupported_reason,omitempty"`
}

type IndexColumn struct {
	Name       *string `json:"name,omitempty"`
	Expression *string `json:"expression,omitempty"`
	Desc       bool    `json:"desc"`
}

type TableMetadata struct {
	Columns     []Column     `json:"columns"`
	PrimaryKey  *PrimaryKey  `json:"primary_key,omitempty"`
	ForeignKeys []ForeignKey `json:"foreign_keys,omitempty"`
	Indexes     []Index      `json:"indexes,omitempty"`
}
