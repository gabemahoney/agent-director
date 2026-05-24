/**
 * FreeGuard — single-free wrapper for native pointers.
 *
 * Every C-ABI call that returns a pointer must pass it to `ad_free_cstring`
 * exactly once. FreeGuard enforces this:
 *
 *   const guard = new FreeGuard(rawPtr, (p) => lib.symbols.ad_free_cstring(p));
 *   try {
 *     const str = new CString(guard.ptr!).toString();
 *     return str;
 *   } finally {
 *     guard.free();  // safe to call even on error path; no-op on second call
 *   }
 *
 * Design constraints:
 *   - No bun:ffi imports — accepts the free function as a constructor arg so
 *     tests can stub it without loading a native library.
 *   - Generic over T so the same guard works for Pointer (branded number) or
 *     any other pointer-like primitive without widening to `any`.
 *   - `free()` nulls the internal reference before calling the underlying
 *     free function, so a synchronous exception inside the free function
 *     cannot cause a double-free on a subsequent call.
 */
export class FreeGuard<T> {
  private _ptr: T | null;
  private readonly _free: (p: T) => void;

  constructor(ptr: T, free: (p: T) => void) {
    this._ptr = ptr;
    this._free = free;
  }

  /**
   * ptr returns the guarded pointer, or null if `free()` has already been
   * called. Callers must check for null before dereferencing.
   */
  get ptr(): T | null {
    return this._ptr;
  }

  /**
   * free calls the underlying free function with the guarded pointer exactly
   * once. After the first call, the internal reference is nulled and
   * subsequent calls are silent no-ops.
   */
  free(): void {
    if (this._ptr === null) return;
    const p = this._ptr;
    this._ptr = null;
    this._free(p);
  }
}
