<#
.SYNOPSIS
  LLM Tester Windows 构建脚本
.DESCRIPTION
  构建 Windows amd64 可执行文件
  如需跨平台构建，在 WSL 中使用: make build-all
#>

$AppName = "llm-tester"
$MainPath = ".\cmd\llm-tester"
$Version = if (git describe --tags --always --dirty 2>$null) { git describe --tags --always --dirty } else { "dev" }
$LdFlags = "-s -w -X main.version=$Version"

Write-Host "🔨 构建 ${AppName} v${Version} (windows/amd64)..." -ForegroundColor Cyan
Write-Host ""

go build -ldflags="$LdFlags" -o "${AppName}.exe" $MainPath
if ($LASTEXITCODE -ne 0) {
    Write-Host "❌ 构建失败" -ForegroundColor Red
    exit 1
}

Write-Host "✅ 构建完成: .\${AppName}.exe" -ForegroundColor Green
Write-Host "   $ .\${AppName}.exe"
Write-Host "   → http://localhost:8912"