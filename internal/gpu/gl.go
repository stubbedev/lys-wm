package gpu

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	NoError                       = 0
	Texture2D                     = 0x0DE1
	TextureExternalOES            = 0x8D65
	Texture0                      = 0x84C0
	TextureMinFilter              = 0x2801
	TextureMagFilter              = 0x2800
	TextureWrapS                  = 0x2802
	TextureWrapT                  = 0x2803
	Linear                        = 0x2601
	Nearest                       = 0x2600
	ClampToEdge                   = 0x812F
	RGBA                          = 0x1908
	BGRAExt                       = 0x80E1
	RGBA8OES                      = 0x8058
	UnsignedByte                  = 0x1401
	Float                         = 0x1406
	UnpackAlignment               = 0x0CF5
	PackRowLengthNV               = 0x0D02
	UnpackRowLengthExt            = 0x0CF2
	UnpackSkipPixelsExt           = 0x0CF4
	UnpackSkipRowsExt             = 0x0CF3
	Framebuffer                   = 0x8D40
	Renderbuffer                  = 0x8D41
	ColorAttachment0              = 0x8CE0
	FramebufferComplete           = 0x8CD5
	Blend                         = 0x0BE2
	ScissorTest                   = 0x0C11
	Zero                          = 0
	One                           = 1
	OneMinusSrcAlpha              = 0x0303
	ColorBufferBit                = 0x4000
	VertexShader                  = 0x8B31
	FragmentShader                = 0x8B30
	CompileStatus                 = 0x8B81
	LinkStatus                    = 0x8B82
	InfoLogLength                 = 0x8B84
	TriangleStrip                 = 0x0005
	ArrayBuffer                   = 0x8892
	StaticDraw                    = 0x88E4
	StreamDraw                    = 0x88E0
	StreamRead                    = 0x88E1
	PixelPackBuffer               = 0x88EB
	MapReadBit                    = 0x0001
	glVendor                      = 0x1F00
	glRenderer                    = 0x1F01
	glVersion                     = 0x1F02
	glExtensions                  = 0x1F03
	glInvalidEnum                 = 0x0500
	glInvalidValue                = 0x0501
	glInvalidOperation            = 0x0502
	glOutOfMemory                 = 0x0505
	glInvalidFramebufferOperation = 0x0506
	glContextLost                 = 0x0507
)

var glErrorNames = map[uint32]string{
	glInvalidEnum: "GL_INVALID_ENUM", glInvalidValue: "GL_INVALID_VALUE", glInvalidOperation: "GL_INVALID_OPERATION",
	glOutOfMemory: "GL_OUT_OF_MEMORY", glInvalidFramebufferOperation: "GL_INVALID_FRAMEBUFFER_OPERATION",
	glContextLost: "GL_CONTEXT_LOST",
}

type GLError struct {
	Op   string
	Code uint32
}

func (e *GLError) Is(target error) bool {
	return target == ErrContextLost && e.Code == glContextLost
}

func (e *GLError) Error() string {
	name, ok := glErrorNames[e.Code]
	if !ok {
		name = fmt.Sprintf("0x%x", e.Code)
	}
	return fmt.Sprintf("gpu: %s: %s", e.Op, name)
}

type GL struct {
	pGetError                uintptr
	getString                func(name uint32) string
	GenTextures              func(n int32, textures *uint32)
	DeleteTextures           func(n int32, textures *uint32)
	bindTexture              uintptr
	pActiveTexture           uintptr
	pTexParameteri           uintptr
	texImage2D               func(target uint32, level, internalFormat, width, height, border int32, format, typ uint32, pixels *byte)
	texSubImage2D            func(target uint32, level, x, y, width, height int32, format, typ uint32, pixels *byte)
	PixelStorei              func(pname uint32, param int32)
	GenFramebuffers          func(n int32, fbs *uint32)
	DeleteFramebuffers       func(n int32, fbs *uint32)
	pBindFramebuffer         uintptr
	FramebufferTexture2D     func(target, attachment, texTarget, texture uint32, level int32)
	FramebufferRenderbuffer  func(target, attachment, rbTarget, rb uint32)
	CheckFramebufferStatus   func(target uint32) uint32
	GenRenderbuffers         func(n int32, rbs *uint32)
	DeleteRenderbuffers      func(n int32, rbs *uint32)
	BindRenderbuffer         func(target, rb uint32)
	RenderbufferStorage      func(target, internalFormat uint32, width, height int32)
	GenBuffers               func(n int32, buffers *uint32)
	DeleteBuffers            func(n int32, buffers *uint32)
	pBindBuffer              uintptr
	bufferData               uintptr
	pViewport                uintptr
	scissor                  uintptr
	pEnable                  uintptr
	pDisable                 uintptr
	pBlendFunc               uintptr
	ClearColor               func(r, g, b, a float32)
	Clear                    func(mask uint32)
	CreateShader             func(typ uint32) uint32
	shaderSource             func(shader uint32, count int32, sources **byte, lengths *int32)
	CompileShader            func(shader uint32)
	GetShaderiv              func(shader, pname uint32, params *int32)
	getShaderInfoLog         func(shader uint32, size int32, length *int32, log *byte)
	DeleteShader             func(shader uint32)
	CreateProgram            func() uint32
	AttachShader             func(program, shader uint32)
	LinkProgram              func(program uint32)
	GetProgramiv             func(program, pname uint32, params *int32)
	getProgramInfoLog        func(program uint32, size int32, length *int32, log *byte)
	pUseProgram              uintptr
	DeleteProgram            func(program uint32)
	GetUniformLocation       func(program uint32, name string) int32
	GetAttribLocation        func(program uint32, name string) int32
	Uniform1i                func(location, v int32)
	pUniform1f               uintptr
	pUniform2f               uintptr
	pUniform4f               uintptr
	uniformMatrix3fv         func(location, count int32, transpose uint8, value *float32)
	pVertexAttribPointer     uintptr
	pEnableVertexAttribArray uintptr
	DisableVertexAttribArray func(index uint32)
	drawArrays               uintptr
	args                     [6]uintptr
	floats                   [4]float32
	readPixels               func(x, y, width, height int32, format, typ uint32, pixels *byte)
	mapBufferRange           func(target uint32, offset, length int, access uint32) unsafe.Pointer
	unmapBuffer              func(target uint32) uint8
	pFlush                   uintptr
	Finish                   func()
	imageTargetTexture2D     func(target uint32, image uintptr)
	imageTargetRenderbuffer  func(target uint32, image uintptr)
}

func (gl *GL) load(l *Library, path string) error {
	if err := bind(l.glesLib, path, []symbol{
		{&gl.pGetError, "glGetError"},
		{&gl.getString, "glGetString"},
		{&gl.GenTextures, "glGenTextures"},
		{&gl.DeleteTextures, "glDeleteTextures"},
		{&gl.bindTexture, "glBindTexture"},
		{&gl.pActiveTexture, "glActiveTexture"},
		{&gl.pTexParameteri, "glTexParameteri"},
		{&gl.texImage2D, "glTexImage2D"},
		{&gl.texSubImage2D, "glTexSubImage2D"},
		{&gl.PixelStorei, "glPixelStorei"},
		{&gl.GenFramebuffers, "glGenFramebuffers"},
		{&gl.DeleteFramebuffers, "glDeleteFramebuffers"},
		{&gl.pBindFramebuffer, "glBindFramebuffer"},
		{&gl.FramebufferTexture2D, "glFramebufferTexture2D"},
		{&gl.FramebufferRenderbuffer, "glFramebufferRenderbuffer"},
		{&gl.CheckFramebufferStatus, "glCheckFramebufferStatus"},
		{&gl.GenRenderbuffers, "glGenRenderbuffers"},
		{&gl.DeleteRenderbuffers, "glDeleteRenderbuffers"},
		{&gl.BindRenderbuffer, "glBindRenderbuffer"},
		{&gl.RenderbufferStorage, "glRenderbufferStorage"},
		{&gl.GenBuffers, "glGenBuffers"},
		{&gl.DeleteBuffers, "glDeleteBuffers"},
		{&gl.pBindBuffer, "glBindBuffer"},
		{&gl.bufferData, "glBufferData"},
		{&gl.pViewport, "glViewport"},
		{&gl.scissor, "glScissor"},
		{&gl.pEnable, "glEnable"},
		{&gl.pDisable, "glDisable"},
		{&gl.pBlendFunc, "glBlendFunc"},
		{&gl.ClearColor, "glClearColor"},
		{&gl.Clear, "glClear"},
		{&gl.CreateShader, "glCreateShader"},
		{&gl.shaderSource, "glShaderSource"},
		{&gl.CompileShader, "glCompileShader"},
		{&gl.GetShaderiv, "glGetShaderiv"},
		{&gl.getShaderInfoLog, "glGetShaderInfoLog"},
		{&gl.DeleteShader, "glDeleteShader"},
		{&gl.CreateProgram, "glCreateProgram"},
		{&gl.AttachShader, "glAttachShader"},
		{&gl.LinkProgram, "glLinkProgram"},
		{&gl.GetProgramiv, "glGetProgramiv"},
		{&gl.getProgramInfoLog, "glGetProgramInfoLog"},
		{&gl.pUseProgram, "glUseProgram"},
		{&gl.DeleteProgram, "glDeleteProgram"},
		{&gl.GetUniformLocation, "glGetUniformLocation"},
		{&gl.GetAttribLocation, "glGetAttribLocation"},
		{&gl.Uniform1i, "glUniform1i"},
		{&gl.pUniform1f, "glUniform1fv"},
		{&gl.pUniform2f, "glUniform2fv"},
		{&gl.pUniform4f, "glUniform4fv"},
		{&gl.uniformMatrix3fv, "glUniformMatrix3fv"},
		{&gl.pVertexAttribPointer, "glVertexAttribPointer"},
		{&gl.pEnableVertexAttribArray, "glEnableVertexAttribArray"},
		{&gl.DisableVertexAttribArray, "glDisableVertexAttribArray"},
		{&gl.drawArrays, "glDrawArrays"},
		{&gl.readPixels, "glReadPixels"},
		{&gl.pFlush, "glFlush"},
		{&gl.Finish, "glFinish"},
	}); err != nil {
		return err
	}
	return errors.Join(
		l.proc(symbol{&gl.imageTargetTexture2D, "glEGLImageTargetTexture2DOES"}),
		l.proc(symbol{&gl.imageTargetRenderbuffer, "glEGLImageTargetRenderbufferStorageOES"}),
		l.proc(symbol{&gl.mapBufferRange, "glMapBufferRange"}),
		l.proc(symbol{&gl.unmapBuffer, "glUnmapBuffer"}),
	)
}

const maxErrorFlags = 16

func (gl *GL) Check(op string) error {
	if code := gl.getError(); code != NoError {
		for range maxErrorFlags {
			if gl.getError() == NoError {
				break
			}
		}
		return &GLError{Op: op, Code: code}
	}
	return nil
}

func (gl *GL) Vendor() string     { return gl.getString(glVendor) }
func (gl *GL) Renderer() string   { return gl.getString(glRenderer) }
func (gl *GL) Version() string    { return gl.getString(glVersion) }
func (gl *GL) Extensions() string { return gl.getString(glExtensions) }

func (gl *GL) TexImage2D(target uint32, internalFormat int32, width, height int32, format uint32, pixels []byte) {
	gl.texImage2D(target, 0, internalFormat, width, height, 0, format, UnsignedByte, first(pixels))
	runtime.KeepAlive(pixels)
}

func (gl *GL) TexSubImage2D(target uint32, x, y, width, height int32, format uint32, pixels []byte) {
	gl.texSubImage2D(target, 0, x, y, width, height, format, UnsignedByte, first(pixels))
	runtime.KeepAlive(pixels)
}

func (gl *GL) ReadPixels(x, y, width, height int32, format uint32, pixels []byte) {
	gl.readPixels(x, y, width, height, format, UnsignedByte, first(pixels))
	runtime.KeepAlive(pixels)
}

func (gl *GL) ReadPixelsIntoBuffer(x, y, width, height int32, format uint32) {
	gl.readPixels(x, y, width, height, format, UnsignedByte, nil)
}

func (gl *GL) AllocateBuffer(target uint32, size int, usage uint32) {
	gl.args[0], gl.args[1], gl.args[2], gl.args[3] = uintptr(target), uintptr(size), 0, uintptr(usage)
	gl.call(gl.bufferData, 4)
}

func (gl *GL) MapBufferForReading(target uint32, size int) []byte {
	p := gl.mapBufferRange(target, 0, size, MapReadBit)
	if p == nil {
		return nil
	}
	return unsafe.Slice((*byte)(p), size) //nolint:gosec // glMapBufferRange mapped size bytes at p
}

func (gl *GL) UnmapBuffer(target uint32) bool {
	return gl.unmapBuffer(target) != 0
}

func (gl *GL) getError() uint32 {
	r, _, _ := purego.SyscallN(gl.pGetError, gl.args[:0]...)
	return uint32(r) //nolint:gosec // a GLenum fits uint32
}

func (gl *GL) ActiveTexture(texture uint32) {
	gl.args[0] = uintptr(texture)
	gl.call(gl.pActiveTexture, 1)
}

func (gl *GL) TexParameteri(target, pname uint32, param int32) {
	gl.args[0], gl.args[1], gl.args[2] = uintptr(target), uintptr(pname), uintptr(param) //nolint:gosec // GLint arguments travel in full registers
	gl.call(gl.pTexParameteri, 3)
}

func (gl *GL) BindFramebuffer(target, fb uint32) {
	gl.args[0], gl.args[1] = uintptr(target), uintptr(fb)
	gl.call(gl.pBindFramebuffer, 2)
}

func (gl *GL) BindBuffer(target, buffer uint32) {
	gl.args[0], gl.args[1] = uintptr(target), uintptr(buffer)
	gl.call(gl.pBindBuffer, 2)
}

func (gl *GL) Viewport(x, y, width, height int32) {
	gl.args[0], gl.args[1], gl.args[2], gl.args[3] = uintptr(x), uintptr(y), uintptr(width), uintptr(height) //nolint:gosec // GLint arguments travel in full registers
	gl.call(gl.pViewport, 4)
}

func (gl *GL) Enable(capability uint32) {
	gl.args[0] = uintptr(capability)
	gl.call(gl.pEnable, 1)
}

func (gl *GL) Disable(capability uint32) {
	gl.args[0] = uintptr(capability)
	gl.call(gl.pDisable, 1)
}

func (gl *GL) BlendFunc(src, dst uint32) {
	gl.args[0], gl.args[1] = uintptr(src), uintptr(dst)
	gl.call(gl.pBlendFunc, 2)
}

func (gl *GL) UseProgram(program uint32) {
	gl.args[0] = uintptr(program)
	gl.call(gl.pUseProgram, 1)
}

func (gl *GL) VertexAttribPointer(index uint32, size int32, typ uint32, normalized uint8, stride int32, offset uintptr) {
	gl.args[0], gl.args[1], gl.args[2], gl.args[3], gl.args[4], gl.args[5] = uintptr(index), uintptr(size), uintptr(typ), uintptr(normalized), uintptr(stride), offset //nolint:gosec // GLint arguments travel in full registers
	gl.call(gl.pVertexAttribPointer, 6)
}

func (gl *GL) EnableVertexAttribArray(index uint32) {
	gl.args[0] = uintptr(index)
	gl.call(gl.pEnableVertexAttribArray, 1)
}

func (gl *GL) Flush() {
	gl.call(gl.pFlush, 0)
}

func (gl *GL) Uniform1f(location int32, v float32) {
	gl.floats[0] = v
	gl.args[0], gl.args[1], gl.args[2] = uintptr(location), 1, uintptr(unsafe.Pointer(&gl.floats[0])) //nolint:gosec // the floats live in the GL value
	gl.call(gl.pUniform1f, 3)
}

func (gl *GL) Uniform2f(location int32, a, b float32) {
	gl.floats[0], gl.floats[1] = a, b
	gl.args[0], gl.args[1], gl.args[2] = uintptr(location), 1, uintptr(unsafe.Pointer(&gl.floats[0])) //nolint:gosec // the floats live in the GL value
	gl.call(gl.pUniform2f, 3)
}

func (gl *GL) Uniform4f(location int32, a, b, c, d float32) {
	gl.floats[0], gl.floats[1], gl.floats[2], gl.floats[3] = a, b, c, d
	gl.args[0], gl.args[1], gl.args[2] = uintptr(location), 1, uintptr(unsafe.Pointer(&gl.floats[0])) //nolint:gosec // the floats live in the GL value
	gl.call(gl.pUniform4f, 3)
}

func (gl *GL) call(fn uintptr, n int) {
	purego.SyscallN(fn, gl.args[:n]...)
}

func (gl *GL) BindTexture(target, texture uint32) {
	gl.args[0], gl.args[1] = uintptr(target), uintptr(texture)
	gl.call(gl.bindTexture, 2)
}

func (gl *GL) Scissor(x, y, width, height int32) {
	gl.args[0], gl.args[1], gl.args[2], gl.args[3] = uintptr(x), uintptr(y), uintptr(width), uintptr(height) //nolint:gosec // GLint arguments travel in full registers
	gl.call(gl.scissor, 4)
}

func (gl *GL) DrawArrays(mode uint32, first, count int32) {
	gl.args[0], gl.args[1], gl.args[2] = uintptr(mode), uintptr(first), uintptr(count) //nolint:gosec // GLint arguments travel in full registers
	gl.call(gl.drawArrays, 3)
}

func (gl *GL) BufferData(target uint32, values []float32, usage uint32) {
	gl.args[0], gl.args[1], gl.args[2], gl.args[3] = uintptr(target), uintptr(len(values)*4), uintptr(unsafe.Pointer(first(values))), uintptr(usage) //nolint:gosec // the vertices stay alive past the call via KeepAlive
	gl.call(gl.bufferData, 4)
	runtime.KeepAlive(values)
}

func (gl *GL) UniformMatrix3(location int32, m *[9]float32) {
	gl.uniformMatrix3fv(location, 1, 0, &m[0])
	runtime.KeepAlive(m)
}

func (gl *GL) EGLImageTargetTexture2D(target uint32, image *Image) {
	gl.imageTargetTexture2D(target, image.handle())
}

func (gl *GL) EGLImageTargetRenderbuffer(image *Image) {
	gl.imageTargetRenderbuffer(Renderbuffer, image.handle())
}

func first[T any](s []T) *T {
	if len(s) == 0 {
		return nil
	}
	return &s[0]
}

func (gl *GL) Compile(typ uint32, source string) (uint32, error) {
	shader := gl.CreateShader(typ)
	if shader == 0 {
		return 0, gl.Check("glCreateShader")
	}
	src := append([]byte(source), 0)
	ptr := &src[0]
	gl.shaderSource(shader, 1, &ptr, nil)
	runtime.KeepAlive(src)
	gl.CompileShader(shader)
	var ok int32
	gl.GetShaderiv(shader, CompileStatus, &ok)
	if ok == 0 {
		log := gl.infoLog(shader, gl.GetShaderiv, gl.getShaderInfoLog)
		gl.DeleteShader(shader)
		return 0, fmt.Errorf("gpu: compile shader: %s", log)
	}
	return shader, nil
}

func (gl *GL) Link(vertex, fragment string) (uint32, error) {
	vs, err := gl.Compile(VertexShader, vertex)
	if err != nil {
		return 0, err
	}
	defer gl.DeleteShader(vs)
	fs, err := gl.Compile(FragmentShader, fragment)
	if err != nil {
		return 0, err
	}
	defer gl.DeleteShader(fs)
	program := gl.CreateProgram()
	gl.AttachShader(program, vs)
	gl.AttachShader(program, fs)
	gl.LinkProgram(program)
	var ok int32
	gl.GetProgramiv(program, LinkStatus, &ok)
	if ok == 0 {
		log := gl.infoLog(program, gl.GetProgramiv, gl.getProgramInfoLog)
		gl.DeleteProgram(program)
		return 0, fmt.Errorf("gpu: link program: %s", log)
	}
	return program, nil
}

func (gl *GL) infoLog(object uint32, get func(uint32, uint32, *int32), read func(uint32, int32, *int32, *byte)) string {
	var n int32
	get(object, InfoLogLength, &n)
	if n <= 1 {
		return "no log"
	}
	buf := make([]byte, n)
	read(object, n, &n, &buf[0])
	return string(buf[:n])
}
