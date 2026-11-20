package codec

// shimsJS provides the handful of host objects vendor payload formatters expect
// to find. They are not conveniences: formatters written for Node-derived
// runtimes use Buffer and ieee754 constantly, and without them a correct vendor
// codec fails with "Buffer is not defined" and looks like a broken device.
//
// ChirpStack solves the same problem by vendoring the real npm packages (buffer,
// base64-js, ieee754) and loading them as ES modules. goja has no module system,
// so these are hand-written equivalents covering the parts formatters actually
// touch, exposed both as globals and through a restricted require().
//
// The surface is deliberately bounded. require() resolves three names and
// refuses everything else with a message naming what is missing, because the
// alternative is a formatter failing with "require is not defined" and no clue
// about what the runtime can provide.
//
// The whole shim is one IIFE: the encoding helpers above are in scope for the
// Buffer and ieee754 definitions below, and nothing leaks into the formatter's
// global object except the four names at the end.
const shimsJS = `
(function () {
  var B64CHARS = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';

  function toBytes(x) {
    if (x === null || x === undefined) { return []; }
    if (typeof x === 'string') { return utf8Decode(x); }
    if (typeof x.length === 'number') {
      var out = [];
      for (var i = 0; i < x.length; i++) { out.push(x[i] & 0xff); }
      return out;
    }
    return [];
  }

  function hexEncode(bytes) {
    var s = '';
    for (var i = 0; i < bytes.length; i++) {
      s += ('0' + (bytes[i] & 0xff).toString(16)).slice(-2);
    }
    return s;
  }

  function hexDecode(str) {
    var out = [];
    for (var i = 0; i + 1 < str.length; i += 2) {
      out.push(parseInt(str.substr(i, 2), 16));
    }
    return out;
  }

  function b64Encode(bytes) {
    var s = '';
    for (var i = 0; i < bytes.length; i += 3) {
      var c0 = bytes[i] & 0xff;
      var c1 = bytes[i + 1];
      var c2 = bytes[i + 2];
      s += B64CHARS.charAt(c0 >> 2);
      s += B64CHARS.charAt(((c0 & 3) << 4) | ((c1 === undefined ? 0 : c1) >> 4));
      s += (c1 === undefined) ? '=' : B64CHARS.charAt(((c1 & 15) << 2) | ((c2 === undefined ? 0 : c2) >> 6));
      s += (c2 === undefined) ? '=' : B64CHARS.charAt(c2 & 63);
    }
    return s;
  }

  function b64Decode(str) {
    var clean = String(str).replace(/[^A-Za-z0-9+/]/g, '');
    var out = [];
    for (var i = 0; i < clean.length; i += 4) {
      var e0 = B64CHARS.indexOf(clean.charAt(i));
      var e1 = B64CHARS.indexOf(clean.charAt(i + 1));
      var e2 = B64CHARS.indexOf(clean.charAt(i + 2));
      var e3 = B64CHARS.indexOf(clean.charAt(i + 3));
      out.push((e0 << 2) | (e1 >> 4));
      if (e2 >= 0) { out.push(((e1 & 15) << 4) | (e2 >> 2)); }
      if (e3 >= 0) { out.push(((e2 & 3) << 6) | e3); }
    }
    return out;
  }

  function utf8Encode(str) {
    var out = [];
    for (var i = 0; i < str.length; i++) {
      var c = str.charCodeAt(i);
      if (c < 0x80) { out.push(c); }
      else if (c < 0x800) {
        out.push(0xc0 | (c >> 6), 0x80 | (c & 0x3f));
      } else {
        out.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 0x3f), 0x80 | (c & 0x3f));
      }
    }
    return out;
  }

  function utf8Decode(bytes) {
    if (typeof bytes === 'string') {
      var s = [];
      for (var k = 0; k < bytes.length; k++) { s.push(bytes.charCodeAt(k) & 0xff); }
      bytes = s;
    }
    var out = '';
    for (var i = 0; i < bytes.length;) {
      var b = bytes[i] & 0xff;
      if (b < 0x80) { out += String.fromCharCode(b); i += 1; }
      else if (b >= 0xc0 && i + 1 < bytes.length) {
        out += String.fromCharCode(((b & 0x1f) << 6) | (bytes[i + 1] & 0x3f));
        i += 2;
      } else if (i + 2 < bytes.length) {
        out += String.fromCharCode(((b & 0x0f) << 12) |
          ((bytes[i + 1] & 0x3f) << 6) | (bytes[i + 2] & 0x3f));
        i += 3;
      } else { i += 1; }
    }
    return out;
  }

  // A Buffer is a plain array with methods attached, so indexing, length,
  // Array.from and .map keep working exactly as a formatter expects them to.
  function mkBuffer(bytes) {
    var a = Array.prototype.slice.call(bytes);
    a.__isBuffer = true;

    a.toString = function (enc) {
      if (enc === 'hex') { return hexEncode(a); }
      if (enc === 'base64') { return b64Encode(a); }
      return utf8Decode(a);
    };
    // Explicitly Array.prototype.slice, not a.slice: a.slice is being replaced
    // here, and calling it would recurse.
    a.slice = function (from, to) {
      return mkBuffer(Array.prototype.slice.call(a, from, to));
    };
    a.toJSON = function () { return Array.prototype.slice.call(a); };

    var view = function () { return new DataView(Uint8Array.from(a).buffer); };
    a.readUInt8 = function (o) { return view().getUint8(o); };
    a.readInt8 = function (o) { return view().getInt8(o); };
    a.readUInt16BE = function (o) { return view().getUint16(o, false); };
    a.readUInt16LE = function (o) { return view().getUint16(o, true); };
    a.readInt16BE = function (o) { return view().getInt16(o, false); };
    a.readInt16LE = function (o) { return view().getInt16(o, true); };
    a.readUInt32BE = function (o) { return view().getUint32(o, false); };
    a.readUInt32LE = function (o) { return view().getUint32(o, true); };
    a.readInt32BE = function (o) { return view().getInt32(o, false); };
    a.readInt32LE = function (o) { return view().getInt32(o, true); };
    a.readFloatBE = function (o) { return view().getFloat32(o, false); };
    a.readFloatLE = function (o) { return view().getFloat32(o, true); };
    a.readDoubleBE = function (o) { return view().getFloat64(o, false); };
    a.readDoubleLE = function (o) { return view().getFloat64(o, true); };
    return a;
  }

  var Buffer = {
    from: function (input, enc) {
      if (typeof input === 'string') {
        if (enc === 'hex') { return mkBuffer(hexDecode(input)); }
        if (enc === 'base64') { return mkBuffer(b64Decode(input)); }
        return mkBuffer(utf8Encode(input));
      }
      return mkBuffer(toBytes(input));
    },
    alloc: function (n) {
      var a = [];
      for (var i = 0; i < n; i++) { a.push(0); }
      return mkBuffer(a);
    },
    concat: function (list) {
      var all = [];
      for (var i = 0; i < list.length; i++) {
        var part = toBytes(list[i]);
        for (var j = 0; j < part.length; j++) { all.push(part[j]); }
      }
      return mkBuffer(all);
    },
    isBuffer: function (x) { return !!(x && x.__isBuffer); }
  };

  var ieee754 = {
    read: function (buf, offset, isLE, mLen, nBytes) {
      var v = new DataView(Uint8Array.from(toBytes(buf)).buffer);
      if (nBytes === 4) { return v.getFloat32(offset, isLE); }
      if (nBytes === 8) { return v.getFloat64(offset, isLE); }
      throw new Error('ieee754: unsupported width ' + nBytes);
    },
    write: function (buf, value, offset, isLE, mLen, nBytes) {
      var bytes = toBytes(buf);
      while (bytes.length < offset + nBytes) { bytes.push(0); }
      var v = new DataView(Uint8Array.from(bytes).buffer);
      if (nBytes === 4) { v.setFloat32(offset, value, isLE); }
      else { v.setFloat64(offset, value, isLE); }
      var u8 = new Uint8Array(v.buffer);
      for (var i = 0; i < u8.length; i++) { buf[i] = u8[i]; }
      return offset + nBytes;
    }
  };

  var base64js = {
    toByteArray: function (str) { return b64Decode(str); },
    fromByteArray: function (bytes) { return b64Encode(toBytes(bytes)); }
  };

  globalThis.Buffer = Buffer;
  globalThis.ieee754 = ieee754;
  globalThis.base64js = base64js;

  globalThis.require = function (name) {
    if (name === 'buffer') { return { Buffer: Buffer }; }
    if (name === 'ieee754') { return ieee754; }
    if (name === 'base64-js') { return base64js; }
    throw new Error("payload formatter required '" + name +
      "', which this server does not provide; only 'buffer', 'ieee754' and " +
      "'base64-js' are available");
  };
}());
`
