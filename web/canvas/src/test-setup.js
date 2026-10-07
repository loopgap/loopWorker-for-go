// jsdom does not implement the layout APIs the canvas measures itself with, so
// React Flow's store throws on the first render without them. Stubbing them is
// the whole job of this file - anything more elaborate would be a maintenance
// liability that breaks on the next React Flow upgrade rather than telling us
// something about our own code.

if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

if (!globalThis.DOMMatrixReadOnly) {
  globalThis.DOMMatrixReadOnly = class {
    constructor(init = '') {
      const nums = String(init).match(/-?[\d.eE+]+/g) || []
      const n = (i) => parseFloat(nums[i] || '1')
      this.a = n(0); this.b = 0; this.c = 0; this.d = n(3); this.e = n(4); this.f = n(5)
      this.m11 = this.a; this.m12 = this.b; this.m13 = 0
      this.m21 = this.c; this.m22 = this.d; this.m23 = 0
      this.m31 = 0; this.m32 = 0; this.m33 = 1
      this.m41 = this.e; this.m42 = this.f; this.m43 = 0
    }
    multiply() { return this }
    inverse() { return this }
    translate() { return this }
    scale() { return this }
    transformPoint(p = {}) { return { x: p.x || 0, y: p.y || 0, z: p.z || 0, w: p.w || 1 } }
  }
}

if (!globalThis.SVGElement.prototype.getBBox) {
  globalThis.SVGElement.prototype.getBBox = () => ({ x: 0, y: 0, width: 0, height: 0 })
}

// jsdom's fetch is absent in some configurations; every test that talks to the
// API installs its own stub, and this keeps an accidental real call from
// escaping to the network.
if (!globalThis.fetch) {
  globalThis.fetch = () => Promise.reject(new Error('fetch not stubbed in this test'))
}