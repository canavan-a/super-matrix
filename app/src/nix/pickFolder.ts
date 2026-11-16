/** Picks a folder's files (path → text). Only the web app can; null elsewhere. */
export async function pickFolder(): Promise<Record<string, string> | null> {
  return null;
}

export const canPickFolder = false;
