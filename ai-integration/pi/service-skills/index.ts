// Talus Service Skills — pi extension
//
// Injects the Talus service directory (name + description + usage_guide excerpt)
// into the CURRENT LLM call only when the user message mentions services.
//
// Design (zero accumulation):
// - Conditional trigger: only when the latest user message matches service
//   keywords (service/服务/deploy/dokploy/portainer/...) or names a registered
//   service. Unrelated turns are untouched.
// - Injected via the `context` event by returning a modified `messages` array
//   (deep copy, "modify messages non-destructively") — affects the current
//   LLM call only, is NEVER written to conversation history, so context does
//   not grow turn over turn.
// - Silent skip: no TALUS_API_KEY, unreachable Talus, empty service list, or
//   any error → no injection, conversation untouched.
//
// Install: copy this directory to ~/.pi/agent/extensions/ (auto-discovered)
// Config:  TALUS_URL (default http://localhost:8080) + TALUS_API_KEY

const TALUS_URL = process.env.TALUS_URL || "http://localhost:8080";
const TALUS_API_KEY = process.env.TALUS_API_KEY || "";

const TTL_MS = 60_000;
let cache = { ts: 0, text: "", names: [] as string[] };

// Keywords that hint the user is talking about services. Keep them broad —
// a false positive only costs a tiny per-turn injection, a false negative
// costs the whole point of the feature.
// 注意：“服务”用负向断言排除“服务器”等复合词，避免纯运维查询误触发。
const SERVICE_KEYWORDS = [
	/service|services|relay|proxy|deploy/i,
	/服务(?!器|端|商)/,
	/代理|应用|业务|面板/,
	/dokploy|portainer|grafana/i,
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
		const names: string[] = [];
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
				"<service-skills-directory>\n可用服务 / Available services（调用某个服务前，先 GET /services/{id} 读取 usage_guide / read its usage_guide before calling）：\n" +
				lines.join("\n") +
				"\n</service-skills-directory>",
		};
		return cache;
	} catch {
		return { text: "", names: [] };
	}
}

function shouldInject(text: string, serviceNames: string[]): boolean {
	if (!text) return false;
	for (const re of SERVICE_KEYWORDS) {
		if (re.test(text)) return true;
	}
	const lower = text.toLowerCase();
	for (const name of serviceNames) {
		if (name && lower.includes(name.toLowerCase())) return true;
	}
	return false;
}

// Extract the last user message text from the context event messages.
function lastUserText(messages: any[]): string {
	for (let i = messages.length - 1; i >= 0; i--) {
		const m = messages[i];
		if (m?.role !== "user") continue;
		const content = m.content;
		if (Array.isArray(content)) {
			const text = content
				.filter((c: any) => c?.type === "text" && typeof c.text === "string")
				.map((c: any) => c.text)
				.join("\n");
			if (text) return text;
		} else if (typeof content === "string") {
			return content;
		}
	}
	return "";
}

export default function serviceSkillsExtension(pi: any) {
	if (!pi.on) return;
	pi.on("context", async (event: any) => {
		try {
			const dir = await fetchServiceDirectory();
			if (!dir.text) return undefined;
			const userText = lastUserText(event?.messages);
			if (!shouldInject(userText, dir.names)) return undefined;
			// Return a modified messages array: deep copy from the event, with a
			// system message appended. Affects only the current LLM call.
			const messages = Array.isArray(event?.messages)
				? [...event.messages]
				: [];
			messages.push({
				role: "system",
				content: [{ type: "text", text: dir.text }],
			});
			return { messages };
		} catch {
			return undefined;
		}
	});
}
