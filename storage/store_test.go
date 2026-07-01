package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// 在每个测试前创建临时目录作为 HOME，避免污染用户配置
func setupTestStore(t *testing.T) *Store {
	t.Helper()
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })

	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() 失败: %v", err)
	}
	return store
}

func TestNewStore_CreatesDir(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Unsetenv("HOME") })

	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() 失败: %v", err)
	}

	// 验证目录已创建
	expectedDir := filepath.Join(tmpDir, ".llm_tester")
	info, err := os.Stat(expectedDir)
	if err != nil {
		t.Fatalf("配置目录未创建: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("路径不是目录")
	}
	// 验证权限 (0700)
	if info.Mode().Perm() != 0700 {
		t.Errorf("目录权限应为 0700, 实际为 %o", info.Mode().Perm())
	}

	// 验证文件路径
	expectedFile := filepath.Join(expectedDir, "configs.json")
	if store.filePath != expectedFile {
		t.Errorf("filePath = %q, 期望 %q", store.filePath, expectedFile)
	}
}

func TestSaveAndLoad(t *testing.T) {
	store := setupTestStore(t)

	cfg := &Config{
		Name:    "test-cfg",
		APIType: "openai",
		BaseURL: "https://api.test.com",
		APIKey:  "sk-test-key",
		Model:   "gpt-4",
	}

	if err := store.Save("test-cfg", cfg); err != nil {
		t.Fatalf("Save() 失败: %v", err)
	}

	// 验证持久化文件存在
	if _, err := os.Stat(store.filePath); os.IsNotExist(err) {
		t.Fatal("配置文件未创建")
	}

	// 模拟重启：新建 Store 实例并验证配置还在
	store2, err := NewStore()
	if err != nil {
		t.Fatalf("第二次 NewStore() 失败: %v", err)
	}

	loaded := store2.Get("test-cfg")
	if loaded == nil {
		t.Fatal("重启后配置丢失")
	}
	if loaded.APIKey != "sk-test-key" {
		t.Errorf("APIKey = %q, 期望 sk-test-key", loaded.APIKey)
	}
	if loaded.APIType != "openai" {
		t.Errorf("APIType = %q, 期望 openai", loaded.APIType)
	}
}

func TestSaveOverwrite(t *testing.T) {
	store := setupTestStore(t)

	org := &Config{Name: "cfg", BaseURL: "https://original.com", APIKey: "key1"}
	if err := store.Save("cfg", org); err != nil {
		t.Fatalf("第一次 Save() 失败: %v", err)
	}

	upd := &Config{Name: "cfg", BaseURL: "https://updated.com", APIKey: "key2"}
	if err := store.Save("cfg", upd); err != nil {
		t.Fatalf("第二次 Save() 失败: %v", err)
	}

	// 验证被覆盖
	loaded := store.Get("cfg")
	if loaded.BaseURL != "https://updated.com" {
		t.Errorf("BaseURL = %q, 期望 updated", loaded.BaseURL)
	}
}

func TestDelete(t *testing.T) {
	store := setupTestStore(t)

	cfg := &Config{Name: "del-me", BaseURL: "https://test.com", APIKey: "k"}
	if err := store.Save("del-me", cfg); err != nil {
		t.Fatalf("Save() 失败: %v", err)
	}

	if err := store.Delete("del-me"); err != nil {
		t.Fatalf("Delete() 失败: %v", err)
	}

	if got := store.Get("del-me"); got != nil {
		t.Error("删除后配置仍存在")
	}

	// 验证文件中也已删除
	store2, _ := NewStore()
	if got := store2.Get("del-me"); got != nil {
		t.Error("重启后已删除的配置仍存在")
	}
}

func TestDeleteNonExistent(t *testing.T) {
	store := setupTestStore(t)
	// 删除不存在的配置不应报错
	if err := store.Delete("nonexistent"); err != nil {
		t.Errorf("删除不存在的配置返回错误: %v", err)
	}
}

func TestList(t *testing.T) {
	store := setupTestStore(t)
	names := []string{"c", "b", "a"}
	for _, n := range names {
		store.Save(n, &Config{Name: n, BaseURL: "https://x.com", APIKey: "k"})
	}

	list := store.List()
	// 验证排序
	expected := []string{"a", "b", "c"}
	for i, name := range list {
		if name != expected[i] {
			t.Errorf("list[%d] = %q, 期望 %q", i, name, expected[i])
		}
	}
}

func TestListEmpty(t *testing.T) {
	store := setupTestStore(t)
	list := store.List()
	if len(list) != 0 {
		t.Errorf("空存储应返回空列表, 得到 %v", list)
	}
}

func TestGetAll(t *testing.T) {
	store := setupTestStore(t)
	store.Save("a", &Config{Name: "a", BaseURL: "https://a.com", APIKey: "k1"})
	store.Save("b", &Config{Name: "b", BaseURL: "https://b.com", APIKey: "k2"})

	all := store.GetAll()
	if len(all) != 2 {
		t.Errorf("GetAll 返回 %d 个配置, 期望 2", len(all))
	}
	if all["a"].APIKey != "k1" {
		t.Errorf("a.APIKey = %q", all["a"].APIKey)
	}
}

func TestSaveAll(t *testing.T) {
	store := setupTestStore(t)

	configs := map[string]*Config{
		"x": {Name: "x", BaseURL: "https://x.com", APIKey: "kx"},
		"y": {Name: "y", BaseURL: "https://y.com", APIKey: "ky"},
	}
	if err := store.SaveAll(configs); err != nil {
		t.Fatalf("SaveAll() 失败: %v", err)
	}

	if store.Get("x") == nil || store.Get("y") == nil {
		t.Error("SaveAll 后配置未保存")
	}
}

func TestImportJSON_Single(t *testing.T) {
	store := setupTestStore(t)
	jsonData := []byte(`{"name":"imported","api_type":"openai","base_url":"https://imp.com","api_key":"ik"}`)

	count, err := store.ImportJSON(jsonData)
	if err != nil {
		t.Fatalf("ImportJSON() 失败: %v", err)
	}
	if count != 1 {
		t.Errorf("导入数量 = %d, 期望 1", count)
	}
	if store.Get("imported") == nil {
		t.Error("导入后配置不存在")
	}
}

func TestImportJSON_Array(t *testing.T) {
	store := setupTestStore(t)
	jsonData := []byte(`[
		{"name":"a","base_url":"https://a.com","api_key":"ka"},
		{"name":"b","base_url":"https://b.com","api_key":"kb"}
	]`)

	count, err := store.ImportJSON(jsonData)
	if err != nil {
		t.Fatalf("ImportJSON() 失败: %v", err)
	}
	if count != 2 {
		t.Errorf("导入数量 = %d, 期望 2", count)
	}
}

func TestImportJSON_Invalid(t *testing.T) {
	store := setupTestStore(t)
	_, err := store.ImportJSON([]byte(`not json`))
	if err == nil {
		t.Error("无效 JSON 应返回错误")
	}
}

func TestExportJSON(t *testing.T) {
	store := setupTestStore(t)
	store.Save("e1", &Config{Name: "e1", APIType: "openai", BaseURL: "https://e1.com", APIKey: "ek1", Model: "m1"})

	data := store.ExportJSON()
	if len(data) == 0 {
		t.Fatal("ExportJSON 返回空数据")
	}

	// 导出的 JSON 格式为 {"configs": {"e1": {...}}}
	// 验证包含配置数据
	if !containsStr(string(data), "e1") {
		t.Error("导出数据中应包含配置名称 e1")
	}
	if !containsStr(string(data), "ek1") {
		t.Error("导出数据中应包含 API Key")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"完整配置", Config{Name: "t", APIType: "openai", BaseURL: "https://x.com", APIKey: "k", Model: "m"}, false},
		{"缺名称", Config{APIType: "openai", BaseURL: "https://x.com", APIKey: "k", Model: "m"}, true},
		{"缺类型", Config{Name: "t", BaseURL: "https://x.com", APIKey: "k", Model: "m"}, true},
		{"openai 缺 URL", Config{Name: "t", APIType: "openai", APIKey: "k", Model: "m"}, true},
		{"openai 缺 Key", Config{Name: "t", APIType: "openai", BaseURL: "https://x.com", Model: "m"}, true},
		{"openai 缺模型", Config{Name: "t", APIType: "openai", BaseURL: "https://x.com", APIKey: "k"}, true},
		{"ollama 只需名称", Config{Name: "t", APIType: "ollama"}, false},
		{"custom 缺 URL", Config{Name: "t", APIType: "custom"}, true},
		{"custom 有 URL", Config{Name: "t", APIType: "custom", BaseURL: "https://x.com"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestPresetsCount(t *testing.T) {
	presets := Presets()
	if len(presets) != 8 {
		t.Errorf("预设数量 = %d, 期望 8", len(presets))
	}
}

func TestPresetsContent(t *testing.T) {
	presets := Presets()
	for _, p := range presets {
		if p.Name == "" {
			t.Error("预设缺少名称")
		}
		if p.APIType == "" {
			t.Errorf("预设 %q 缺少 api_type", p.Name)
		}
	}
}

func TestFilePermission(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Unsetenv("HOME") })

	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() 失败: %v", err)
	}

	store.Save("perm-test", &Config{Name: "perm-test", BaseURL: "https://x.com", APIKey: "secret"})

	info, err := os.Stat(store.filePath)
	if err != nil {
		t.Fatalf("Stat 配置文件失败: %v", err)
	}
	// 验证权限为 0600
	if info.Mode().Perm() != 0600 {
		t.Errorf("文件权限应为 0600, 实际为 %o", info.Mode().Perm())
	}
}

func TestSaveLoadEscape(t *testing.T) {
	// 测试包含特殊字符的配置名
	store := setupTestStore(t)
	name := "特殊/字符:测试"
	cfg := &Config{Name: name, BaseURL: "https://x.com", APIKey: "k"}

	if err := store.Save(name, cfg); err != nil {
		t.Fatalf("Save(特殊名) 失败: %v", err)
	}

	loaded := store.Get(name)
	if loaded == nil {
		t.Error("特殊名称配置丢失")
	}
}

func TestExportContainsAPIKey(t *testing.T) {
	store := setupTestStore(t)
	store.Save("key-test", &Config{Name: "key-test", BaseURL: "https://x.com", APIKey: "my-secret-key"})

	data := store.ExportJSON()
	if !containsStr(string(data), "my-secret-key") {
		t.Error("导出应包含 API Key")
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && containsStrInner(s, substr)
}

func containsStrInner(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
