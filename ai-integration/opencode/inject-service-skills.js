// Talus Service Skills — OpenCode plugin
//
// Injects the Talus service directory (name + description + usage_guide excerpt)
// into every user message, so the AI always sees what services are available and
// is reminded to fetch the per-service usage guide before relaying.
//
// Silent-skip behavior: no TALUS_API_KEY, unreachable Talus, empty service list,
// or any error → nothing is injected and the conversation is untouched.
//
// Install: copy this file to ~/.config/opencode/plugins/
// Config:  TALUS_URL (default http://localhost:8080) + TALUS_API_KEY

const TALUS_URL = process.env.TALUS_URL || "http://localhost:8080"
const TALUS_API_KEY = process.env.TALUS_API_KEY || ""

const TTL_MS = 60_000
let cache = { ts: 0, text: "" }

async function fetchServiceDirectory() {
  if (Date.now() - cache.ts < TTL_MS) return cache.text
  if (!TALUS_API_KEY) return ""
  try {
    const resp = await fetch(`${TALUS_URL}/api/v1/services`, {
      headers: { "X-API-Key": TALUS_API_KEY },
      signal: AbortSignal.timeout(5000),
    })
    if (!resp.ok) return ""
    const payload = await resp.json()
    const data = Array.isArray(payload) ? payload : payload?.data
    if (!Array.isArray(data) || data.length === 0) return ""
    const lines = data.map((s) => {
      const desc = s.description ?? ""
      const excerpt = s.usage_guide_excerpt ?? "无"
      return `- ${s.name} — ${desc} | 指南: ${excerpt}`
    })
    cache = {
      ts: Date.now(),
      text:
        "<service-skills-directory>\n可用服务（调用某个服务前，先 GET /services/{id} 读取 usage_guide）：\n" +
        lines.join("\n") +
        "\n</service-skills-directory>",
    }
    return cache.text
  } catch {
    return ""
  }
}

export default async () => {
  return {
    "chat.message": async (input, output) => {
      try {
        const block = await fetchServiceDirectory()
        if (!block) return
        const parts = output?.parts || []
        const i = parts.findIndex((p) => p.type === "text" && p.text !== undefined)
        if (i !== -1) {
          parts[i].text = `${block}\n\n${parts[i].text}`
        } else {
          parts.unshift({ type: "text", text: block })
        }
      } catch {
        // never break the conversation
      }
    },
  }
}
