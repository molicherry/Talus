// Talus Service Skills — OpenCode plugin
//
// Injects the Talus service directory (name + description + usage_guide excerpt)
// into the CURRENT LLM request only when the user message mentions services.
//
// Design (zero accumulation):
// - Conditional trigger: only when the latest user message matches service
//   keywords (service/服务/deploy/dokploy/portainer/...) or names a registered
//   service. Unrelated turns are untouched.
// - Injected via `experimental.chat.messages.transform`, which modifies the
//   messages array sent to the LLM for THIS call only — it is never written
//   to conversation history, so context does not grow turn over turn.
// - Silent skip: no TALUS_API_KEY, unreachable Talus, empty service list, or
//   any error → no injection, conversation untouched.
//
// Install: copy this file to ~/.config/opencode/plugins/
// Config:  TALUS_URL (default http://localhost:8080) + TALUS_API_KEY

const TALUS_URL = process.env.TALUS_URL || "http://localhost:8080";
const TALUS_API_KEY = process.env.TALUS_API_KEY || "";

const TTL_MS = 60_000;
let cache = { ts: 0, text: "", names: [] };

// Keywords that hint the user is talking about services. Keep them broad —
// a false positive only costs a tiny per-turn injection, a false negative
// costs the whole point of the feature.
const SERVICE_KEYWORDS = [
	"service",
	"services",
	"relay",
	"proxy",
	"deploy",
	"部署",
	"服务",
	"代理",
	"应用",
	"业务",
	"面板",
	"dokploy",
	"portainer",
	"grafana",
];

async function fetchServiceDirectory() {
	const now = Date.now();
	if (now - cache.ts < TTL_MS) return cache;
	if (!TALUS_API_KEY) return { text: "", names: [] };
	try {
		const resp = await fetch(`${TALUS_URL}/api/v1/services`, {
			headers: { "X-API-Key": TALUS_API_KEY },
			signal: AbortSignal.timeout(5000),
		});
		if (!resp.ok) return { text: "", names: [] };
		const payload = await resp.json();
		const data = Array.isArray(payload) ? payload : payload?.data;
		if (!Array.isArray(data) || data.length === 0)
			return { text: "", names: [] };
		const names = [];
		const lines = data.map((s) => {
			names.push(String(s.name ?? ""));
			const desc = s.description ?? "";
			const excerpt = s.usage_guide_excerpt ?? "无";
			return `- ${s.name} — ${desc} | 指南: ${excerpt}`;
		});
		cache = {
			ts: now,
			names,
			text:
				"<service-skills-directory>\n可用服务（调用某个服务前，先 GET /services/{id} 读取 usage_guide）：\n" +
				lines.join("\n") +
				"\n</service-skills-directory>",
		};
		return cache;
	} catch {
		return { text: "", names: [] };
	}
}

function shouldInject(text, serviceNames) {
	if (!text) return false;
	const lower = text.toLowerCase();
	for (const kw of SERVICE_KEYWORDS) {
		if (lower.includes(kw)) return true;
	}
	for (const name of serviceNames) {
		if (name && lower.includes(String(name).toLowerCase())) return true;
	}
	return false;
}

function lastUserText(messages) {
	for (let i = messages.length - 1; i >= 0; i--) {
		const m = messages[i];
		if (m?.info?.role !== "user") continue;
		const parts = m.parts || [];
		const text = parts
			.filter((p) => p?.type === "text" && typeof p.text === "string")
			.map((p) => p.text)
			.join("\n");
		if (text) return text;
	}
	return "";
}

export default async () => {
	return {
		"experimental.chat.messages.transform": async (input, output) => {
			try {
				const dir = await fetchServiceDirectory();
				if (!dir.text) return;
				const messages = Array.isArray(output?.messages) ? output.messages : [];
				const userText = lastUserText(messages);
				if (!shouldInject(userText, dir.names)) return;
				// Append a system message for THIS LLM call only — never persisted.
				messages.push({
					info: { role: "system" },
					parts: [{ type: "text", text: dir.text }],
				});
			} catch {
				// never break the conversation
			}
		},
	};
};
