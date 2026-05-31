globalThis.setSafeHTML = function setSafeHTML(target, html) {
  const parsed = new DOMParser().parseFromString(String(html), "text/html");
  sanitizeHTML(parsed.body);
  target.replaceChildren(...parsed.body.childNodes);
};

function sanitizeHTML(root) {
  root.querySelectorAll("script, style, iframe, object, embed, base, meta").forEach((node) => node.remove());
  root.querySelectorAll("*").forEach((node) => {
    Array.from(node.attributes).forEach((attribute) => {
      const name = attribute.name.toLowerCase();
      if (name.startsWith("on") || name === "srcdoc") {
        node.removeAttribute(attribute.name);
        return;
      }
      if (["href", "src", "action", "formaction"].includes(name) && !safeURL(attribute.value)) {
        node.removeAttribute(attribute.name);
      }
    });
  });
}

function safeURL(value) {
  const normalized = String(value || "").trim().toLowerCase();
  return normalized === "" || normalized.startsWith("/") || normalized.startsWith("https://") || normalized.startsWith("data:image/");
}
