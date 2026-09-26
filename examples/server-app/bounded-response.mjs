// SPDX-License-Identifier: Apache-2.0

export async function readBoundedText(response, maximumBytes, label = "ContextBridge response") {
  if (!Number.isSafeInteger(maximumBytes) || maximumBytes <= 0) {
    throw new RangeError("maximumBytes must be a positive safe integer");
  }
  const contentLength = response.headers.get("content-length");
  if (contentLength !== null) {
    const declared = Number(contentLength);
    if (Number.isFinite(declared) && declared > maximumBytes) {
      if (response.body && typeof response.body.cancel === "function") {
        await response.body.cancel(`${label} declared byte limit exceeded`);
      }
      throw new Error(`${label} exceeds ${maximumBytes} bytes`);
    }
  }
  if (!response.body || typeof response.body.getReader !== "function") {
    throw new Error(`runtime cannot enforce the ${label} byte limit`);
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let total = 0;
  let text = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.byteLength;
      if (total > maximumBytes) {
        await reader.cancel(`${label} byte limit exceeded`);
        throw new Error(`${label} exceeds ${maximumBytes} bytes`);
      }
      text += decoder.decode(value, { stream: true });
    }
    return text + decoder.decode();
  } finally {
    reader.releaseLock();
  }
}
