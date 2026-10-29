# lh

`lh` is a keyboard-first HTTP client for the terminal. It brings a polished request workspace, saved projects, file uploads, and readable responses to a single native TUI.

## Why lh

- Compose requests with a method picker plus structured Params, Auth, Headers, Body, and Files sections.
- Switch persistent named environments without exposing their values in the picker.
- Keep related requests in persistent projects; history remains a separate activity log.
- Switch among eight considered themes, including three light palettes and five dark palettes.
- Focus the request form or response pane when you need room, or use the split workbench.
- Paste confidently: rich-text whitespace and smart quotes are normalized before they break a URL, header, or JSON payload.
- Browse formatted JSON, response headers, timing, size, and content type without leaving the terminal.

## Install

Requires Go 1.21 or newer.

```bash
git clone https://github.com/jhapriyansh/lighthouse.git lh
cd lh
go build -o lh .
./lh
```

Or run it directly:

```bash
go run .
```

## Request form

The top request form is deliberately compact:

1. Choose a method with the arrow controls.
2. Enter a URL. A missing scheme becomes `https://` automatically; local addresses use `http://`.
3. Use the **Params** section for `name=value` pairs; each becomes a safely encoded query parameter.
4. Use **Auth** for `Bearer {{TOKEN}}` or a bare token (which becomes a Bearer token).
5. Add one header per line as `Name: value`, write a body, and press `F9` to format JSON.
6. Use **Files** to attach `field=@/absolute/path/to/file`.
7. Press `Enter` in the URL field, `Ctrl+Enter` anywhere, or `F5` to send.

File attachments are sent as `multipart/form-data`. If a body is also present, it is included as a multipart field named `payload`. The generated cURL command represents attachments with `-F`.

### Header completion and safe paste

In the Headers editor, start typing a standard header name on a new line and press `Ctrl+Space` to complete it. Most terminals report that combination as `Ctrl+@`; `Alt+Enter` is provided as an equivalent fallback. Common request headers including `Accept`, `Authorization`, `Content-Type`, and `X-API-Key` are included.

Pasted input is normalized to prevent common terminal/request mistakes: Windows line endings, non-breaking spaces, zero-width characters, and typographic quotes are cleaned in URL, header, body, and upload fields.

## Projects, history, and environments

Projects store reusable request definitions; history is a global chronological list.

| Action | Shortcut |
| --- | --- |
| Open projects | `Ctrl+P` |
| Create a project from the current request | `n` in Projects |
| Save current request to the open project | `s` in Projects |
| Open a project | `Enter` in Projects |
| Open history | `F2` / `Ctrl+O` |
| Pin current request | `F3` |
| Open environments | `Ctrl+E` |

Open the environment manager with `Ctrl+E`. Press `n` to create one, `e` to edit it, and `Enter` to activate it. In the editor, set the first line as `# name: Staging`, add `NAME=value` lines, then press `Ctrl+S` to save. Values stay masked in the environment list. `{{NAME}}` resolves from the selected environment first, then from the process environment:

```text
URL:           {{API_HOST}}/v1/assets
Authorization: Bearer {{API_TOKEN}}
Upload:        image=@{{HOME}}/Pictures/logo.png
```

Request history and projects retain template text—not expanded secret values. Set `LH_HISTORY_FILE`, `LH_PROJECTS_FILE`, or `LH_ENVIRONMENTS_FILE` to choose their locations.

## Workbench controls

| Key | Action |
| --- | --- |
| `Tab` / `Shift+Tab` | Move between form and response controls |
| `Ctrl+Enter`, `F5`, `Ctrl+R` | Send request |
| `Esc` | Cancel an in-flight request |
| `F8` / `Ctrl+T` | Toggle response body and headers |
| `F10` | Cycle split, response-focus, and request-focus panes |
| `Ctrl+G` | Choose a theme |
| `F4` | Copy response |
| `F6` | Copy cURL command |
| `F7` | Save response to a file |
| `F9` | Format JSON body |
| `F1` | Help |

The UI is fully mouse-friendly: click the method selector, fields, pane tabs, toolbar, and modal rows; use the wheel to scroll a response.

## Response behavior

Responses are read up to 10 MB, followed across redirects, and time out after 60 seconds. JSON is formatted and colorized automatically. Plain response text, formatted JSON, and response headers can all be copied or saved.

## Storage

By default, lh writes private JSON files to your OS configuration directory:

| Data | Default file |
| --- | --- |
| History | `lh/history.json` |
| Projects | `lh/projects.json` |
| Environments | `lh/environments.json` |

The app is built with Bubble Tea, Bubbles, and Lip Gloss.
