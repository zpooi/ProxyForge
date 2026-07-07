export async function fetchJSON(url, options) {
  const response = await fetch(url, options);
  if (!response.ok) throw new Error(await response.text() || String(response.status));
  return response.json();
}
