package mapping

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------
// §5.1 @{payload.field} — 字段引用取值 (resolveString)
// ---------------------------------------------------------------

// @test-case TC3.7-pure_field_ref
// @test-case TC3.7-nested_path
// @test-case TC3.7-missing_field
// @test-case TC3.7-non_map_intermediate
// @test-case TC3.7-static_template
// @test-case TC3.7-mixed_template
func TestEngine_ResolveString(t *testing.T) {
	e := &Engine{}

	t.Run("5.1.1 pure @{payload.field} returns string representation", func(t *testing.T) {
		tests := []struct {
			name     string
			payload  map[string]any
			template string
			want     string
		}{
			{name: "string value", payload: map[string]any{"order_id": "123"}, template: "@{payload.order_id}", want: "123"},
			{name: "integer value", payload: map[string]any{"count": 42}, template: "@{payload.count}", want: "42"},
			{name: "float value", payload: map[string]any{"price": 29.99}, template: "@{payload.price}", want: "29.99"},
			{name: "bool value", payload: map[string]any{"active": true}, template: "@{payload.active}", want: "true"},
			{name: "nil value", payload: map[string]any{"note": nil}, template: "@{payload.note}", want: ""},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := e.resolveString(tt.template, tt.payload)
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			})
		}
	})

	t.Run("5.1.2 nested path a.b.c", func(t *testing.T) {
		payload := map[string]any{
			"a": map[string]any{
				"b": map[string]any{
					"c": "v",
				},
			},
		}
		got, err := e.resolveString("@{payload.a.b.c}", payload)
		assert.NoError(t, err)
		assert.Equal(t, "v", got)
	})

	t.Run("5.1.3 missing field returns empty string", func(t *testing.T) {
		payload := map[string]any{}
		got, err := e.resolveString("@{payload.missing}", payload)
		assert.NoError(t, err)
		assert.Equal(t, "", got)
	})

	t.Run("5.1.4 non-map intermediate returns empty string", func(t *testing.T) {
		payload := map[string]any{"a": "string"}
		got, err := e.resolveString("@{payload.a.b}", payload)
		assert.NoError(t, err)
		assert.Equal(t, "", got)
	})

	t.Run("5.1.5 static string (no template references)", func(t *testing.T) {
		payload := map[string]any{}
		got, err := e.resolveString("static-value", payload)
		assert.NoError(t, err)
		assert.Equal(t, "static-value", got)
	})

	t.Run("5.1.6 mixed template with prefix", func(t *testing.T) {
		payload := map[string]any{"id": "123"}
		got, err := e.resolveString("user-@{payload.id}", payload)
		assert.NoError(t, err)
		assert.Equal(t, "user-123", got)
	})

	t.Run("mixed template with suffix", func(t *testing.T) {
		payload := map[string]any{"token": "abc"}
		got, err := e.resolveString("@{payload.token}-suffix", payload)
		assert.NoError(t, err)
		assert.Equal(t, "abc-suffix", got)
	})

	t.Run("multiple @{} references in one template", func(t *testing.T) {
		payload := map[string]any{
			"first": "John",
			"last":  "Doe",
		}
		got, err := e.resolveString("@{payload.first}-@{payload.last}", payload)
		assert.NoError(t, err)
		assert.Equal(t, "John-Doe", got)
	})
}

// ---------------------------------------------------------------
// §5.2 $source — 取值来源 (resolveField)
// ---------------------------------------------------------------

// @test-case TC3.7-source_integer
// @test-case TC3.7-source_boolean
// @test-case TC3.7-source_null
// @test-case TC3.7-source_prefix_suffix
func TestEngine_ResolveField(t *testing.T) {
	e := &Engine{}

	t.Run("5.2.1 pure reference keeps integer type", func(t *testing.T) {
		payload := map[string]any{"count": 42}
		got, err := e.resolveField("@{payload.count}", payload)
		require.NoError(t, err)
		assert.Equal(t, 42, got)
	})

	t.Run("5.2.2 pure reference keeps boolean type", func(t *testing.T) {
		payload := map[string]any{"active": true}
		got, err := e.resolveField("@{payload.active}", payload)
		require.NoError(t, err)
		assert.Equal(t, true, got)
	})

	t.Run("5.2.3 pure reference keeps nil", func(t *testing.T) {
		payload := map[string]any{"note": nil}
		got, err := e.resolveField("@{payload.note}", payload)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("5.2.4 with prefix/suffix converts to string", func(t *testing.T) {
		payload := map[string]any{"id": 42}
		got, err := e.resolveField("id_@{payload.id}", payload)
		require.NoError(t, err)
		assert.Equal(t, "id_42", got)
	})

	t.Run("pure reference keeps integer (non-string payload)", func(t *testing.T) {
		payload := map[string]any{"age": int64(25)}
		got, err := e.resolveField("@{payload.age}", payload)
		require.NoError(t, err)
		assert.Equal(t, int64(25), got)
	})

	t.Run("pure reference keeps float64 type", func(t *testing.T) {
		payload := map[string]any{"price": 29.99}
		got, err := e.resolveField("@{payload.price}", payload)
		require.NoError(t, err)
		assert.Equal(t, 29.99, got)
	})

	t.Run("missing field in pure reference returns nil", func(t *testing.T) {
		payload := map[string]any{}
		got, err := e.resolveField("@{payload.missing}", payload)
		require.NoError(t, err)
		assert.Nil(t, got)
	})
}

// ---------------------------------------------------------------
// 辅助函数: getNestedField
// ---------------------------------------------------------------

func TestGetNestedField(t *testing.T) {
	t.Run("direct field access", func(t *testing.T) {
		data := map[string]any{"order_id": "123"}
		got := getNestedField(data, "order_id")
		assert.Equal(t, "123", got)
	})

	t.Run("nested path a.b.c", func(t *testing.T) {
		data := map[string]any{
			"a": map[string]any{
				"b": map[string]any{"c": "v"},
			},
		}
		got := getNestedField(data, "a.b.c")
		assert.Equal(t, "v", got)
	})

	t.Run("missing field returns nil", func(t *testing.T) {
		data := map[string]any{}
		got := getNestedField(data, "missing")
		assert.Nil(t, got)
	})

	t.Run("non-map intermediate returns nil", func(t *testing.T) {
		data := map[string]any{"a": "string"}
		got := getNestedField(data, "a.b")
		assert.Nil(t, got)
	})

	t.Run("empty path returns nil", func(t *testing.T) {
		data := map[string]any{"key": "val"}
		got := getNestedField(data, "")
		assert.Nil(t, got)
	})

	t.Run("deeply nested with missing mid-path", func(t *testing.T) {
		data := map[string]any{
			"a": map[string]any{
				"b": "leaf",
			},
		}
		// a.x.c where x doesn't exist inside a
		got := getNestedField(data, "a.x.c")
		assert.Nil(t, got)
	})

	t.Run("integer value preserved", func(t *testing.T) {
		data := map[string]any{"count": 42}
		got := getNestedField(data, "count")
		assert.Equal(t, 42, got)
	})

	t.Run("boolean value preserved", func(t *testing.T) {
		data := map[string]any{"active": true}
		got := getNestedField(data, "active")
		assert.Equal(t, true, got)
	})

	t.Run("nil value preserved", func(t *testing.T) {
		data := map[string]any{"note": nil}
		got := getNestedField(data, "note")
		assert.Nil(t, got)
	})
}

// ---------------------------------------------------------------
// 辅助函数: tostring
// ---------------------------------------------------------------

func TestTostring(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{name: "string", input: "hello", want: "hello"},
		{name: "integer", input: 42, want: "42"},
		{name: "int64", input: int64(42), want: "42"},
		{name: "float64", input: 3.14, want: "3.14"},
		{name: "bool true", input: true, want: "true"},
		{name: "bool false", input: false, want: "false"},
		{name: "nil", input: nil, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tostring(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------
// §5.3 $type — 强制类型转换 (convertType)
// ---------------------------------------------------------------

// @test-case TC3.7-type_int_to_string
// @test-case TC3.7-type_string_to_int
// @test-case TC3.7-type_string_to_number
// @test-case TC3.7-type_int_to_bool
// @test-case TC3.7-type_invalid_conversion
func TestConvertType(t *testing.T) {
	t.Run("5.3.1 integer to string", func(t *testing.T) {
		got, err := convertType(42, "string")
		require.NoError(t, err)
		assert.Equal(t, "42", got)
	})

	t.Run("5.3.2 string to integer", func(t *testing.T) {
		got, err := convertType("42", "integer")
		require.NoError(t, err)
		assert.Equal(t, int64(42), got)
	})

	t.Run("5.3.3 string to number", func(t *testing.T) {
		got, err := convertType("29.99", "number")
		require.NoError(t, err)
		assert.Equal(t, 29.99, got)
	})

	t.Run("5.3.4 integer to boolean (truthy)", func(t *testing.T) {
		got, err := convertType(1, "boolean")
		require.NoError(t, err)
		assert.Equal(t, true, got)
	})

	t.Run("integer to boolean (falsy)", func(t *testing.T) {
		got, err := convertType(0, "boolean")
		require.NoError(t, err)
		assert.Equal(t, false, got)
	})

	t.Run("5.3.5 invalid string to integer returns error", func(t *testing.T) {
		_, err := convertType("abc", "integer")
		require.Error(t, err)
	})

	t.Run("float64 to integer", func(t *testing.T) {
		got, err := convertType(3.14, "integer")
		require.NoError(t, err)
		assert.Equal(t, int64(3), got)
	})

	t.Run("bool to string", func(t *testing.T) {
		got, err := convertType(true, "string")
		require.NoError(t, err)
		assert.Equal(t, "true", got)
	})

	t.Run("bool to boolean (already bool)", func(t *testing.T) {
		got, err := convertType(false, "boolean")
		require.NoError(t, err)
		assert.Equal(t, false, got)
	})

	t.Run("int64 to boolean (truthy)", func(t *testing.T) {
		got, err := convertType(int64(42), "boolean")
		require.NoError(t, err)
		assert.Equal(t, true, got)
	})

	t.Run("int64 to boolean (falsy)", func(t *testing.T) {
		got, err := convertType(int64(0), "boolean")
		require.NoError(t, err)
		assert.Equal(t, false, got)
	})

	t.Run("string to boolean (true)", func(t *testing.T) {
		got, err := convertType("true", "boolean")
		require.NoError(t, err)
		assert.Equal(t, true, got)
	})

	t.Run("string to boolean (false)", func(t *testing.T) {
		got, err := convertType("false", "boolean")
		require.NoError(t, err)
		assert.Equal(t, false, got)
	})

	t.Run("float64 to number (already float64)", func(t *testing.T) {
		got, err := convertType(99.99, "number")
		require.NoError(t, err)
		assert.Equal(t, 99.99, got)
	})

	t.Run("int to number", func(t *testing.T) {
		got, err := convertType(100, "number")
		require.NoError(t, err)
		assert.Equal(t, 100, got)
	})

	t.Run("invalid string to number returns error", func(t *testing.T) {
		_, err := convertType("not-a-number", "number")
		require.Error(t, err)
	})

	t.Run("nil to string", func(t *testing.T) {
		got, err := convertType(nil, "string")
		require.NoError(t, err)
		assert.Equal(t, "", got)
	})

	t.Run("non-zero float64 converts to true in boolean conversion", func(t *testing.T) {
		got, err := convertType(3.14, "boolean")
		require.NoError(t, err)
		assert.Equal(t, true, got)
	})

	t.Run("unknown type name returns error", func(t *testing.T) {
		_, err := convertType(42, "unknown_type")
		require.Error(t, err)
	})
}

// ---------------------------------------------------------------
// §5.4 $format — 格式转换 (formatValue)
// ---------------------------------------------------------------

// @test-case TC3.7-format_timestamp
func TestFormatValue(t *testing.T) {
	t.Run("5.4.1 unix timestamp to date format", func(t *testing.T) {
		got, err := formatValue("1716518400", "2006-01-02")
		require.NoError(t, err)
		assert.Equal(t, "2024-05-24", got)
	})

	t.Run("0 timestamp returns epoch date", func(t *testing.T) {
		got, err := formatValue("0", "2006-01-02")
		require.NoError(t, err)
		assert.Equal(t, "1970-01-01", got)
	})
}

// ---------------------------------------------------------------
// BuildRequest (smoke test — method is public, engine is stub)
// ---------------------------------------------------------------

func TestEngine_BuildRequest(t *testing.T) {
	e := &Engine{}
	got, err := e.BuildRequest(nil, nil, nil)
	assert.NoError(t, err)
	assert.Nil(t, got)
}
