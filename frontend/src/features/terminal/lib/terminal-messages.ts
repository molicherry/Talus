// Match service.TerminalMessageMaxBytes. The budget covers JSON escaping and
// UTF-8 on the wire, not JavaScript string length or the unescaped paste size.
export const TERMINAL_MESSAGE_MAX_BYTES = 64 << 10;

const encoder = new TextEncoder();
const inputEnvelopeBytes = encoder.encode(JSON.stringify({ type: "input", data: "" })).byteLength;

/** Split input without changing code-point order or splitting a surrogate pair. */
export function* terminalInputChunks(data: string): Generator<string> {
  let chunk: string[] = [];
  let bytes = inputEnvelopeBytes;
  for (const character of data) {
    // JSON.stringify also accounts for escaped controls, quotes, backslashes
    // and lone surrogates. Remove the two enclosing ASCII quote bytes.
    const cost = encoder.encode(JSON.stringify(character)).byteLength - 2;
    if (bytes + cost > TERMINAL_MESSAGE_MAX_BYTES) {
      yield chunk.join("");
      chunk = [];
      bytes = inputEnvelopeBytes;
    }
    chunk.push(character);
    bytes += cost;
  }
  if (chunk.length > 0) yield chunk.join("");
}
