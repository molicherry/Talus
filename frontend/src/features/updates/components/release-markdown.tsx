import Markdown from "react-markdown";

/** Release bodies are Markdown. Raw HTML and images are deliberately excluded. */
export function ReleaseMarkdown({ notes }: { notes: string }) {
  const summary = notes.replace(/^## (?:English|简体中文)[ \t]*(?:\n+|$)/, "");
  return (
    <div className="space-y-3 break-words text-sm leading-relaxed [&_h1]:text-base [&_h1]:font-semibold [&_h2]:text-base [&_h2]:font-semibold [&_h3]:font-semibold [&_h4]:font-semibold [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:list-decimal [&_ol]:pl-5 [&_li]:my-1 [&_a]:text-link [&_a]:underline [&_pre]:overflow-x-auto [&_pre]:rounded-lg [&_pre]:bg-secondary [&_pre]:p-3 [&_code]:font-mono [&_code]:text-xs [&_blockquote]:border-l-2 [&_blockquote]:border-border [&_blockquote]:pl-3">
      <Markdown
        skipHtml
        allowedElements={["h1", "h2", "h3", "h4", "h5", "h6", "p", "ul", "ol", "li", "strong", "em", "code", "pre", "blockquote", "a", "br", "hr"]}
        urlTransform={url => {
          try {
            const parsed = new URL(url, "https://github.com/molicherry/Talus/");
            return parsed.protocol === "https:" ? parsed.href : "";
          } catch {
            return "";
          }
        }}
        components={{
          a: ({ href, children }) => href
            ? <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>
            : <span>{children}</span>,
        }}
      >
        {summary}
      </Markdown>
    </div>
  );
}
