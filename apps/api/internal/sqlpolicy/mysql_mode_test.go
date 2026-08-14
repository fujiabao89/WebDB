package sqlpolicy

import "testing"

func TestMySQLLexerModeFromSession(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    MySQLLexerMode
		wantErr bool
	}{
		{name: "empty", raw: "", want: MySQLLexerMode{}},
		{name: "whitespace-only mode", raw: " \t ", wantErr: true},
		{
			name: "mysql 8 default modes",
			raw:  "ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION",
			want: MySQLLexerMode{},
		},
		{name: "ansi quotes", raw: "STRICT_TRANS_TABLES,ANSI_QUOTES", want: MySQLLexerMode{ANSIQuotes: true}},
		{name: "ansi combination", raw: "ANSI", want: MySQLLexerMode{ANSIQuotes: true}},
		{name: "no backslash escapes", raw: " no_backslash_escapes ", want: MySQLLexerMode{NoBackslashEscapes: true}},
		{name: "traditional combination", raw: "TRADITIONAL", want: MySQLLexerMode{}},
		{name: "unknown future mode", raw: "FUTURE_LEXER_MODE", wantErr: true},
		{name: "empty token", raw: "STRICT_TRANS_TABLES,,NO_ENGINE_SUBSTITUTION", wantErr: true},
		{name: "unsupported syntax mode", raw: "PIPES_AS_CONCAT", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MySQLLexerModeFromSession(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("MySQLLexerModeFromSession(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("MySQLLexerModeFromSession(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestSupportedMySQLLexerMode(t *testing.T) {
	if got := SupportedMySQLLexerMode(); got != (MySQLLexerMode{}) {
		t.Fatalf("SupportedMySQLLexerMode() = %+v, want zero/default mode", got)
	}
}
