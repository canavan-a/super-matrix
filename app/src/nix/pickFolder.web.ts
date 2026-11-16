// On the web, a folder picker: a flake's directory (flake.nix, flake.lock
// and the rest) read into the editor, like `source:` does for the CLI.

const SKIP = /(^|\/)(\.git|\.direnv|node_modules|result)(\/|$)/;
const MAX = 1024 * 1024;

export const canPickFolder = true;

export function pickFolder(): Promise<Record<string, string> | null> {
  return new Promise((resolve, reject) => {
    const input = document.createElement('input');
    input.type = 'file';
    input.multiple = true;
    input.setAttribute('webkitdirectory', '');
    input.onchange = async () => {
      const files = Array.from(input.files ?? []);
      if (files.length === 0) return resolve(null);
      const out: Record<string, string> = {};
      let total = 0;
      for (const f of files) {
        // webkitRelativePath is "<folder>/<path>": drop the folder's name.
        const rel = (f.webkitRelativePath || f.name).split('/').slice(1).join('/') || f.name;
        if (SKIP.test(rel)) continue;
        total += f.size;
        if (total > MAX) return reject(new Error('The folder comes to more than 1 MiB'));
        out[rel] = await f.text();
      }
      resolve(out);
    };
    input.click();
  });
}
