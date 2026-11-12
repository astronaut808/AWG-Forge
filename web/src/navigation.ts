export const navigationEvent = "awg-forge:navigate";

export function navigate(section: string) {
  const hash = `#/${section}`;
  if (globalThis.location.hash === hash) return;
  globalThis.history.pushState(null, "", hash);
  globalThis.dispatchEvent(new Event(navigationEvent));
}
