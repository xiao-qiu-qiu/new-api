# 主页包

主页包决定访客打开站点首页时看到的内容。超级管理员在 **系统设置 → 站点与品牌 → 主页包** 中管理，保存后无需重启服务或重新构建前端。

## 使用

- **官方主页**：保留 New API 原有主页，随时可以切回。
- **CodeQ**：内置备用主页，沿用站点的导航、主题、语言和页脚。点击“复制后编辑”即可修改副本。
- **自定义包**：新建 HTML 包或 URL 包，也可以导入一个 `.json` 文件。

“预览”仅在设置页展示。首次创建时，“保存主页包”保存为备选，“保存并启用”才会切换首页。编辑正在使用的包并保存，会直接更新首页。删除当前包会切回官方主页。

内置包不能被覆盖或删除。导入不会覆盖同 ID 的包；要更新已有包，请选中它编辑。导出 CodeQ 得到包含页面代码、图标和翻译的可编辑副本。

升级不会自动切换主页。原有 `HomePageContent` 会继续使用，设置页也提供切回旧主页内容的入口。

## 文件格式

一个主页包是一个 UTF-8 JSON 文件。例如：

```json
{
  "schemaVersion": 1,
  "id": "my-home",
  "name": "我的主页",
  "kind": "html",
  "layout": "content",
  "html": "<main><h1>Welcome</h1><a href='/pricing'>模型价格</a></main>",
  "css": "body { margin: 0; padding: 48px; font-family: system-ui; color: var(--foreground); background: var(--background); }",
  "javascript": "addEventListener('newapi:context', ({ detail }) => { console.log(detail.siteName); });",
  "config": {},
  "locales": {
    "en": { "Welcome": "Welcome" },
    "zh-CN": { "Welcome": "欢迎使用" }
  }
}
```

| 字段 | 含义 |
| --- | --- |
| `schemaVersion` | 固定为 `1` |
| `id` | 小写字母开头，只能含小写字母、数字和 `-`，最多 64 字符 |
| `name` / `description` | 名称 / 可选说明 |
| `kind` | `html` 或 `url` |
| `layout` | `content` 保留站点导航；`full` 由包提供导航。两种布局都保留站点页脚和项目署名 |
| `html` / `css` / `javascript` | HTML 包的页面代码；`html` 必填 |
| `url` | URL 包的 HTTP(S) 地址，不能夹带用户名、密码；不能同时填写 HTML/CSS/JS |
| `config` | 可选的自定义配置对象 |
| `locales` | 可选翻译字典，以 `en`、`zh-CN`、`zh-TW`、`fr`、`ja`、`ru`、`vi` 为键 |

`official`、`codeq`、`legacy` 是保留 ID。最多保存 32 个自定义包，整个配置不超过 4 MiB。名称最多 100 字符，说明最多 500 字符。

HTML 可以是片段，也可以是完整文档。相对资源地址默认从站点根目录解析，站内链接默认打开顶层页面。图片可用内联 SVG、data URL 或外部 URL；使用 LobeHub 图标时应保留相应授权说明。React、Vue 等项目可先构建，再将产物合并为单文件 HTML，或部署后使用 URL 包。不支持直接导入 ZIP、源码目录或需要 Node 服务端运行的项目。

URL 包示例：

```json
{
  "schemaVersion": 1,
  "id": "external-home",
  "name": "外部主页",
  "kind": "url",
  "layout": "content",
  "url": "https://example.com/landing/"
}
```

目标网站需要允许 iframe 嵌入。没有接入高度消息的外部页面以固定高度显示，可在框内滚动。

## 页面接口

HTML 包在独立 iframe 中运行，支持 JavaScript，但不能读取控制台的 Cookie、登录令牌或父页面 DOM。包内容和配置会公开给首页访客，请勿放入密钥。

宿主加载页面及切换主题、语言时发送 `newapi:context` 事件：

```js
addEventListener('newapi:context', ({ detail }) => {
  // theme: light / dark；language: 标准语言代码
  // siteName、logo、origin、apiBase：站点公开信息
  // config：主页包自定义配置；messages：当前语言字典
  document.querySelector('h1').textContent = detail.config.title || detail.siteName;
});

await NewAPIHomepage.copy('需要复制的内容'); // 返回是否成功
NewAPIHomepage.t('Welcome');                // 当前语言文案
NewAPIHomepage.resize();                    // 内容高度变化后通知宿主
NewAPIHomepage.scrollTo(400);               // 滚动到包内指定纵坐标
```

当前配置也可通过 `NewAPIHomepage.context` 读取。首次值为 `null`，请在事件中初始化依赖站点配置的内容。

宿主会同步 `.dark`、`data-theme`、`lang` 和背景、文字、主色、边框、卡片等 CSS 变量。HTML 的普通文本及 `aria-label`、`title`、`alt` 会按 `locales` 翻译；代码块不参与自动翻译。动态生成的文本请使用 `NewAPIHomepage.t()`。

URL 包可实现相同的 `postMessage` 协议：消息包含 `channel: 'new-api.homepage/v1'`；加载后向父窗口发送 `{channel, type:'ready'}`，接收 `{channel, type:'context', context}`。高度消息为 `{channel, type:'resize', height}`。接收时必须确认 `event.source === parent`。高度限制为 240–30000 像素。

## CodeQ 配置与维护

复制 CodeQ 后，在 JSON 标签页修改 `config`：`brand`、`headline`、`subtitle`、`apiBase` 和 `models`。API 地址留空时跟随当前站点。模型 ID 用 `gpt`、`claude`、`gemini`、`deepseek` 四个键；页面中的代码示例不会发起实际模型调用。

CodeQ 源码在 `web/homepage-packages/codeq/`。修改后运行 `bun run homepage:build` 生成前端按需加载的主页包；正常 `dev`、`build` 也会执行此步骤。品牌图标复用 `@lobehub/icons`，授权见同目录 `NOTICE.txt`。

配置保存在现有 `HomePagePackages` 选项中，使用事务保存，成功后才更新内存。公开接口只返回当前启用的包，不返回备选草稿。已打开的首页每 30 秒检查一次更新，切回窗口也会刷新；同一浏览器中的设置保存会通知其他首页标签页立即更新。多实例部署还需等待原有配置同步周期。

所有管理操作沿用系统设置的超级管理员权限。多人同时编辑时以最后一次成功保存为准。
