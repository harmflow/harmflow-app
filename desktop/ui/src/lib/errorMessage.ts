// The desktop shell rejects a failed command with its message alone, a string, not an Error; the local API's client
// rejects with an Error. A toast or an error line reads either one the same way.

/** The message of what a promise rejected with: an Error's message, or the value itself as text. */
export function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
