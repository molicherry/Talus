import { chromium } from "playwright";
const port = 4182, base = `http://127.0.0.1:${port}`;
const ws = () => {
  class F { static OPEN=1; static CONNECTING=0; static CLOSED=3;
    constructor(url){ this.url=url; this.readyState=0; window.__ws.sockets.push(this); setTimeout(()=>{this.readyState=1; this.onopen?.(new Event("open"));},0); }
    send(d){ window.__ws.frames.push(JSON.parse(d)); }
    close(){ this.readyState=3; this.onclose?.(new CloseEvent("close",{code:1000})); }
    deliver(m){ this.onmessage?.(new MessageEvent("message",{data:JSON.stringify(m)})); } }
  window.__ws = { sockets: [], frames: [], closed: [], last: () => window.__ws.sockets.at(-1) };
  window.WebSocket = F;
};
const browser = await chromium.launch({ headless: true });
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await ctx.addInitScript(ws);
await ctx.addInitScript(() => { localStorage.setItem("auth_token", `test.${btoa(JSON.stringify({uid:1,username:"u",role:"admin",exp:Date.now()/1000+3600}))}.test`); localStorage.setItem("i18nextLng","en"); });
await ctx.route("**/api/**", async (route) => {
  const p = new URL(route.request().url()).pathname;
  const reply = (d, s=200) => route.fulfill({status:s, contentType:"application/json", body: JSON.stringify(s>=400?d:{data:d})}).catch(()=>{});
  if (/^\/api\/v1\/servers\/\d+$/.test(p)) return reply({ id:1, name:"Alpha", host:"h", port:22, owner_id:1, status:"online", created_at:"2026-01-01T00:00:00Z", credential_id:1 });
  return reply([]);
});
const page = await ctx.newPage();
await page.goto(`${base}/servers/1/terminal`);
await page.waitForTimeout(700);
await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
const lines = Array.from({length:120},(_,i)=>`L${i}`).join("\r\n");
await page.evaluate((p) => window.__ws.last().deliver({ type: "output", data: p + "\r\n" }), lines);
await page.waitForTimeout(700);
console.log("viewport:", await page.evaluate(() => { const v=document.querySelector(".xterm-viewport"); return { scrollTop:v.scrollTop, scrollHeight:v.scrollHeight, clientHeight:v.clientHeight }; }));
console.log("rows text:", await page.evaluate(() => (document.querySelector(".xterm-rows")?.innerText ?? "").replace(/\n/g,"|").slice(0,60)));
const box = await page.getByTestId("terminal-surface").boundingBox();
await page.mouse.move(box.x + box.width/2, box.y + box.height/2);
await page.mouse.wheel(0, -3000);
await page.waitForTimeout(400);
console.log("after wheel:", await page.evaluate(() => { const v=document.querySelector(".xterm-viewport"); return { scrollTop:v.scrollTop }; }));
console.log("rows text after:", await page.evaluate(() => (document.querySelector(".xterm-rows")?.innerText ?? "").replace(/\n/g,"|").slice(0,60)));
await browser.close();
