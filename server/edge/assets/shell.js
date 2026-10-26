// The browser shell (server/edge/shell.go): xterm.js on a WebSocket.
// Binary messages are terminal bytes; text messages are JSON.
(function () {
  var dark = !window.matchMedia || !window.matchMedia('(prefers-color-scheme: light)').matches;
  var term = new Terminal({
    cursorBlink: true,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
    fontSize: 14,
    scrollback: 5000,
    theme: dark
      ? {background: '#0d1117', foreground: '#e6edf3', cursor: '#2f81f7', selectionBackground: '#264f78'}
      : {background: '#ffffff', foreground: '#1b1f24', cursor: '#0969da', selectionBackground: '#b6d7ff'},
  });
  var fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(document.getElementById('term'));
  fit.fit();

  var status = document.getElementById('status');
  var enc = new TextEncoder();
  var ws = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/_supermatrix/shell');
  ws.binaryType = 'arraybuffer';

  function sendSize() {
    if (ws.readyState === 1) {
      ws.send(JSON.stringify({type: 'resize', cols: term.cols, rows: term.rows}));
    }
  }
  ws.onopen = function () {
    status.textContent = '';
    sendSize();
    term.focus();
  };
  ws.onmessage = function (ev) {
    if (typeof ev.data === 'string') {
      var m = JSON.parse(ev.data);
      if (m.type === 'exit') {
        status.textContent = m.message + ' Reload to connect again.';
      }
      return;
    }
    term.write(new Uint8Array(ev.data));
  };
  ws.onclose = function () {
    if (!status.textContent) {
      status.textContent = 'Disconnected. Reload to connect again.';
    }
  };
  term.onData(function (d) {
    if (ws.readyState === 1) {
      ws.send(enc.encode(d));
    }
  });
  term.onResize(sendSize);
  window.addEventListener('resize', function () {
    fit.fit();
  });
})();
