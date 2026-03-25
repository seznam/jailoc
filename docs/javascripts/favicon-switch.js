function isDark() {
  var scheme = document.body.getAttribute("data-md-color-scheme");
  if (scheme === "slate") return true;
  if (scheme === "default") return false;
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}

function updateFavicon() {
  var favicon = document.querySelector("link[rel='shortcut icon']")
    || document.querySelector("link[rel='icon']");
  if (favicon) {
    favicon.href = isDark() ? "favicon-dark.png" : "favicon-light.png";
  }
}

var observer = new MutationObserver(function () { updateFavicon(); });
observer.observe(document.body, { attributes: true, attributeFilter: ["data-md-color-scheme"] });
window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", updateFavicon);
updateFavicon();
