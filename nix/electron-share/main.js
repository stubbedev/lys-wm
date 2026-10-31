const { app, BrowserWindow, desktopCapturer, session } = require("electron");
const path = require("path");

const capturerTypes = { monitor: "screen", window: "window" };

app.whenReady().then(() => {
  session.defaultSession.setDisplayMediaRequestHandler((request, callback) => {
    request.frame
      .executeJavaScript("location.hash.slice(1)")
      .then((surface) => {
        if (!(surface in capturerTypes)) {
          throw new Error(`the page asked for surface ${JSON.stringify(surface)}`);
        }
        return desktopCapturer.getSources({ types: [capturerTypes[surface]] });
      })
      .then((sources) => {
        if (sources.length === 0) {
          throw new Error("desktopCapturer found no source");
        }
        callback({ video: sources[0] });
      })
      .catch((err) => {
        console.error(`electron-share: ${err.message}`);
        callback({});
      });
  });
  const seconds = process.argv.at(-1);
  new BrowserWindow({ width: 800, height: 600 }).loadFile(path.join(__dirname, "screencast.html"), { search: `seconds=${seconds}` });
});
