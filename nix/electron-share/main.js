const { app, BrowserWindow, desktopCapturer, session } = require("electron");
const path = require("path");

const capturerTypes = { monitor: "screen", window: "window" };

app.whenReady().then(() => {
  session.defaultSession.setDisplayMediaRequestHandler((request, callback) => {
    const surface = new URL(request.frame.url).hash.slice(1);
    desktopCapturer.getSources({ types: [capturerTypes[surface]] }).then((sources) => callback({ video: sources[0] }));
  });
  const seconds = process.argv.at(-1);
  new BrowserWindow({ width: 800, height: 600 }).loadFile(path.join(__dirname, "screencast.html"), { search: `seconds=${seconds}` });
});
