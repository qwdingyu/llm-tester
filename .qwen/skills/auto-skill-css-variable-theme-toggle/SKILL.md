---
name: css-variable-theme-toggle
description: >-
  CSS 变量 + color-mix + localStorage 实现浅色/深色主题切换，无硬编码颜色漏色
source: auto-skill
extracted_at: '2026-07-02T12:30:00.000Z'
---

# CSS 变量双主题切换模式

纯前端（无构建工具）的浅色/深色主题切换方案。核心：`color-mix()` 动态计算派生色，避免维护两套 badge/hover 等颜色。

## 原理

1. `:root` 定义浅色变量，`.dark` 定义深色变量（同名覆盖）
2. 所有使用处通过 `var(--name)` 引用，不出现硬编码颜色
3. `color-mix()` 将基色与面板背景色混合，自动适配双主题
4. `localStorage` 持久化用户偏好
5. 全局 `transition` 提供平滑切换动画

## 实现步骤

### 1. CSS 变量定义

```css
:root {
  --bg: #f0f2f5;
  --panel: #ffffff;
  --border: #e4e8f0;
  --text: #1a1a2e;
  --text-muted: #6b7280;
  --accent: #3b82f6;
  --success: #22c55e;
  --warning: #f59e0b;
  --danger: #ef4444;
  --info: #6366f1;
}
.dark {
  --bg: #0f172a;
  --panel: #1e293b;
  --border: #334155;
  --text: #e2e8f0;
  --text-muted: #94a3b8;
  --accent: #60a5fa;
  --success: #4ade80;
  --warning: #fbbf24;
  --danger: #f87171;
  --info: #818cf8;
}
```

### 2. 全局 transition 实现平滑切换

```css
* { transition: background-color 0.15s, border-color 0.15s, color 0.15s; }
```

### 3. 使用 color-mix 替代硬编码 badge/hover 颜色

**原理**：`color-mix(in srgb, var(--color) N%, var(--panel))` 将主题色按 N% 透明度混合到面板背景上，自动适配浅色/深色面板。

```css
/* badge 背景色 — 之前硬编码 #dcfce7 / #fee2e2 等 */
.badge-success {
  background: color-mix(in srgb, var(--success) 20%, var(--panel));
  color: var(--success);
}
.badge-danger {
  background: color-mix(in srgb, var(--danger) 20%, var(--panel));
  color: var(--danger);
}
.badge-neutral {
  background: color-mix(in srgb, var(--text-muted) 15%, var(--panel));
  color: var(--text-muted);
}

/* 表格行悬停 — 之前硬编码 #f8fafc */
tbody tr:hover {
  background: color-mix(in srgb, var(--accent) 8%, var(--panel));
}

/* 选中项背景 — 之前硬编码 #eff6ff */
.config-item.active {
  background: color-mix(in srgb, var(--accent) 15%, var(--panel));
}
```

### 4. Vue 3 切换逻辑

```javascript
const isDark = ref(localStorage.getItem('theme') === 'dark');

function applyTheme(dark) {
  document.documentElement.classList.toggle('dark', dark);
}
applyTheme(isDark.value); // 启动时立即应用，不等 onMounted

function toggleTheme() {
  isDark.value = !isDark.value;
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light');
  applyTheme(isDark.value);
}
```

### 5. HTML 切换按钮

```html
<button @click="toggleTheme" :title="isDark ? '浅色模式' : '深色模式'">
  {{ isDark ? '☀️' : '🌙' }}
</button>
```

## 检查漏色的方法

```bash
# 查找所有硬编码颜色（排除 CSS 变量定义）
grep -n '#[0-9a-fA-F]\{3,6\}' index.html | grep -v ":root\|\.dark" | grep -v "var(--"
```

如果输出只有 `:root` 和 `.dark` 中的变量定义，说明没有漏色。

## 注意事项

1. `color-mix()` 是 CSS Color Level 5 特性，Chrome 111+ / Firefox 113+ / Safari 16.2+ 支持，2026 年已可安全使用
2. 不要在 `:root` 或 `.dark` 之外出现硬编码颜色值
3. `transition: all` 对性能有影响，建议只指定需要过渡的属性
4. `localStorage` 的 key 应加项目前缀避免冲突
5. 应用启动时在 `onMounted` **之前**执行 `applyTheme()`，避免闪烁