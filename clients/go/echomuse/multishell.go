package echomuse

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"
)

// MultiShellRunner serves several shell planes as one.
//
// A device often needs more than one thing over the shell plane: the
// link credential, and the wake assets the controller pushes. The SDK's
// default plane carries the credential and nothing else, and a device
// that needs the assets as well has to serve both.
//
// This runner does that. Each plane is asked whether it recognises a
// command, and the first that does answers it. A command none of them
// recognises is refused.
//
// It is a composition, not a shell: no plane in it can run an arbitrary
// command unless it was written to.
type MultiShellRunner struct {
	// Planes are the runners, in the order they are asked.
	Planes []ShellRunner
}

// Start begins a session.
//
// The pty flag is passed to each plane, and a plane that cannot give a
// terminal ignores it.
func (m MultiShellRunner) Start(ctx context.Context, pty bool) (ShellSession, error) {
	sessions := make([]ShellSession, 0, len(m.Planes))
	for _, plane := range m.Planes {
		session, err := plane.Start(ctx, pty)
		if err != nil {
			for _, open := range sessions {
				_ = open.Close()
			}
			return nil, err
		}
		sessions = append(sessions, session)
	}
	composite := &multiShellSession{planes: sessions, carriers: carriersOf(m.Planes)}
	composite.ready = sync.NewCond(&composite.mu)
	composite.queued = sync.NewCond(&composite.mu)
	composite.idle = sync.NewCond(&composite.mu)
	for _, session := range sessions {
		composite.readers = append(composite.readers, newPlaneReader(session))
	}
	composite.worker.Add(1)
	go composite.work()
	return composite, nil
}

// planeReader reads one plane's answers, one at a time.
//
// Each plane has its own reader, so an answer that arrives late — a plane
// that took its time, and answered after the next command was written —
// is the answer to the command it was given and not to the one after it.
type planeReader struct {
	plane ShellSession
	// stopping is closed when the session is closing. A wait for an
	// answer ends at once when it is closed, rather than at the end of
	// the answer timeout.
	stopping chan struct{}
	// mu guards the pending answer, which the reader and the caller
	// both touch.
	mu sync.Mutex
	// answers carries the answers back to the caller.
	answers chan string
	// pending holds an answer that has arrived but not been taken.
	pending string
	// quiet says the pending answer has been quiet long enough to be
	// taken as complete.
	quiet bool
	// closed says the session is closing and no answer is coming.
	closed bool
}

// newPlaneReader starts reading a plane.
func newPlaneReader(plane ShellSession) *planeReader {
	r := &planeReader{
		plane:    plane,
		answers:  make(chan string, 8),
		stopping: make(chan struct{}),
	}
	go r.read()
	return r
}

// read takes the plane's output and frames it into answers.
func (r *planeReader) read() {
	defer close(r.answers)
	buf := make([]byte, 4096)
	for {
		n, err := r.plane.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			r.push(string(chunk))
		}
		if err != nil {
			r.finish()
			return
		}
	}
}

// push takes a piece of a plane's output.
//
// The output is framed on the marker the controller waits for, so an
// answer is the text before the marker. A plane that does not frame its
// answer — a plane an integrator wrote — is read until it goes quiet.
func (r *planeReader) push(chunk string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending += chunk
	for {
		idx := strings.Index(r.pending, CommandSentinel)
		if idx < 0 {
			break
		}
		answer := r.pending[:idx]
		r.pending = r.pending[idx+len(CommandSentinel):]
		r.deliver(answer)
	}
	if r.pending != "" && strings.HasSuffix(r.pending, "\n") {
		// The answer looks complete, and the quiet timer says whether
		// anything more is coming.
		r.quiet = true
	}
}

// deliver hands an answer to the caller.
func (r *planeReader) deliver(answer string) {
	select {
	case r.answers <- answer:
	default:
		// The caller is not keeping up. The answer is dropped rather
		// than the reader blocked on it, because a plane that answers
		// faster than the caller reads is a plane whose answers are
		// stale anyway.
	}
}

// wake ends a wait for an answer.
//
// It is called when the session is closing: the answer will never come,
// and the worker must not wait for it.
func (r *planeReader) wake() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.stopping)
	}
	r.mu.Unlock()
}

// finish says the plane has ended.
func (r *planeReader) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending != "" {
		r.deliver(r.pending)
		r.pending = ""
	}
}

// next takes the next answer, waiting for it if need be.
//
// It reports false once the plane has ended and has nothing more to say,
// and once the session is closing.
//
// An answer that is not framed is taken when nothing has arrived for a
// moment. That is what lets a plane written by an integrator — one that
// does not know about the marker — be used in the composite.
func (r *planeReader) next(timeout time.Duration) (string, bool) {
	// The quiet timer ends the wait for a plane that does not frame its
	// answers.
	deadline := time.Now().Add(timeout)
	for {
		select {
		case answer, ok := <-r.answers:
			if !ok {
				return "", false
			}
			return answer, true
		case <-r.stopping:
			// The session is closing. The answer will never come.
			return "", false
		case <-time.After(answerQuiet):
			r.mu.Lock()
			if r.closed {
				r.mu.Unlock()
				return "", false
			}
			if r.quiet {
				answer := r.pending
				r.pending = ""
				r.quiet = false
				r.mu.Unlock()
				return answer, true
			}
			r.mu.Unlock()
			if time.Now().After(deadline) {
				return "", false
			}
		}
	}
}

// multiShellSession is one session across several planes.
//
// The commands are read as lines and each is offered to every plane in
// turn. The first plane that answers has answered.
//
// The commands are carried out ONE AT A TIME. A shell is serial: the
// controller writes a command and waits for its answer before it writes
// the next, and the commands it writes depend on the answers — the
// digest check that follows a transfer reads the file the transfer
// wrote. A device that carried them out in parallel would have the
// answer to one command read as the answer to another, and a check that
// ran before the transfer it checks would fail every time.
//
// The answer is read on its own goroutine, because a plane's Read blocks
// until it has something to say, and a plane that never says anything
// must not hold the command for ever. Reading it here would also make
// the caller wait on a plane that has decided not to answer — and a
// reader read with io.ReadAll that never ends is a reader that fills
// the machine's memory.
type multiShellSession struct {
	planes []ShellSession
	// carriers says which planes carry a large command. It is a property
	// of the RUNNER and not of the session it made, so it is recorded
	// here rather than asked of the session.
	carriers []bool
	// readers take each plane's answers, one at a time.
	readers []*planeReader

	mu sync.Mutex
	// out is what the session has answered and not yet been read.
	out []byte
	// buffer holds the command that has not all arrived.
	buffer strings.Builder
	// queue holds the commands that have arrived and not been carried out.
	queue []string
	// busy says a command is being carried out.
	busy bool
	// ready wakes a reader when there is something to read.
	ready *sync.Cond
	// queued wakes the worker when a command has arrived.
	queued *sync.Cond
	// idle wakes a waiter when the worker is between commands.
	idle *sync.Cond
	// closed ends the session.
	closed bool
	// closeOnce guards the close.
	closeOnce sync.Once
	// worker counts the goroutine that carries the commands out.
	worker sync.WaitGroup
}

// Read returns what the session has answered.
//
// It blocks until there is something to read, and returns EOF once the
// session is closed.
func (s *multiShellSession) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.out) == 0 {
		if s.closed {
			return 0, io.EOF
		}
		s.ready.Wait()
	}
	n := copy(p, s.out)
	// The buffer is copied out rather than resliced. Reslicing keeps
	// the whole array alive — and appending to a resliced buffer writes
	// into the space the reader has already consumed, so the array
	// grows for ever.
	s.out = append([]byte(nil), s.out[n:]...)
	return n, nil
}

// Write takes commands and answers them.
//
// The answering is done on the worker's own goroutine, so a command that
// a plane takes a moment to answer does not hold up the caller — and a
// plane that never answers does not hold it up at all.
func (s *multiShellSession) Write(p []byte) (int, error) {
	// The input is buffered and only a complete command is acted on.
	//
	// The controller's file transfer is one command spread over hundreds
	// of frames. Acting on every frame would copy the body hundreds of
	// times and start a goroutine for each — which is what filled the
	// machine's memory.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	s.buffer.Write(p)
	sized := s.buffer.Len()
	overflow := sized > maxCompositeBytes
	if overflow {
		s.buffer.Reset()
	}
	s.mu.Unlock()

	if overflow {
		s.say(FrameAnswer("echomuse: refused: the command is larger than this device will hold\n"))
		return len(p), nil
	}

	// The command is acted on when it has arrived WHOLE.
	//
	// A newline is not enough: the controller's file transfer is a
	// heredoc, and its first line ends in one — so a command taken at
	// the first newline is that first line, and every frame after it
	// starts another command. A transfer is megabytes, so that is
	// megabytes copied for every frame of it.
	// The commands in the buffer are taken one at a time, and each is
	// queued for the worker. A heredoc is one command spread over
	// several lines, so the split is on the heredoc's delimiter and not
	// on every newline.
	for {
		command, rest, complete := s.nextCommand()
		if !complete {
			return len(p), nil
		}
		s.setBuffer(rest)
		s.enqueue(command)
	}
}

// enqueue puts a command on the queue and wakes the worker.
func (s *multiShellSession) enqueue(command string) {
	s.mu.Lock()
	s.queue = append(s.queue, command)
	s.mu.Unlock()
	s.queued.Signal()
}

// work carries the commands out, one at a time.
//
// The order is the order they arrived in, and so are the answers: the
// controller's digest check reads the file the transfer before it wrote,
// and a check that ran first would fail every time.
func (s *multiShellSession) work() {
	defer s.worker.Done()
	for {
		command, ok := s.next()
		if !ok {
			s.setIdle()
			return
		}
		s.say(FrameAnswer(s.run(command)))
		s.setIdle()
	}
}

// next takes the next command off the queue.
//
// It blocks until there is one, and reports false once the session is
// closed and the queue is empty.
func (s *multiShellSession) next() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.queue) == 0 {
		if s.closed {
			return "", false
		}
		s.queued.Wait()
	}
	command := s.queue[0]
	s.queue[0] = ""
	s.queue = s.queue[1:]
	s.busy = true
	return command, true
}

// setIdle says the worker is between commands.
func (s *multiShellSession) setIdle() {
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
	s.idle.Broadcast()
}

// nextCommand takes the first complete command off the buffer.
//
// A plain command ends at its newline. A heredoc ends at its delimiter,
// which is named in the heredoc's own line — and it spans several lines,
// so it is one command and not one command per line.
//
// The scan is bounded. The controller sends a base64 transfer line by line
// — 76 bytes a line, so 20 MB is 270,000 frames — and scanning the whole
// buffer on each of them is quadratic in the size of the transfer. The
// head is looked at for the delimiter and the tail for its arrival.
func (s *multiShellSession) nextCommand() (command, rest string, complete bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text := s.buffer.String()

	// The head: is this a heredoc, and what is its delimiter?
	head := text
	if len(head) > 4096 {
		head = head[:4096]
	}
	idx := strings.Index(head, "<<")
	if idx < 0 {
		// A plain command is complete at its newline. The newline is in
		// the head, because a plain command is short.
		nl := strings.Index(head, "\n")
		if nl < 0 {
			return "", text, false
		}
		// The newline is kept with the command. A plane is written one
		// command at a time, and a command without its newline is a
		// command that has not arrived.
		return text[:nl+1], text[nl+1:], true
	}

	// A heredoc: the delimiter is quoted, and the command ends when the
	// delimiter has arrived on a line of its own.
	//
	// The delimiter is looked for in the whole buffer rather than only
	// after the "<<", because the controller writes the redirect before
	// the heredoc: "base64 -d > PATH << 'DELIM'".
	after := text[idx:]
	quote := strings.Index(after, "'")
	if quote < 0 {
		// The quote may be before the "<<" — the redirect and the
		// heredoc can be written either way round.
		before := text[:idx]
		if q := strings.LastIndex(before, "'"); q >= 0 {
			if e := strings.Index(before[q+1:], "'"); e >= 0 {
				return s.heredoc(text, before[q+1:q+1+e])
			}
		}
		return "", text, false
	}
	after = after[quote+1:]
	end := strings.Index(after, "'")
	if end < 0 {
		return "", text, false
	}
	return s.heredoc(text, after[:end])
}

// heredoc takes a heredoc command off the buffer.
//
// The delimiter ends it, and the delimiter is at the end of a transfer — so
// only the tail is worth looking at. The scan is bounded, because the
// controller sends a base64 transfer line by line and scanning the whole
// buffer on each of them is quadratic in the size of the transfer.
//
// The delimiter is looked for in the BODY and not in the whole command,
// because the command line names the delimiter too — "base64 -d << 'DELIM'
// > PATH". Finding it there would end the command at its own first line.
func (s *multiShellSession) heredoc(text, delimiter string) (command, rest string, complete bool) {
	// The lock is held by the caller: this is called from nextCommand,
	// which holds it.

	// The body starts after the command line. The command line ends at
	// its first newline, which is the one after the delimiter was named.
	bodyStart := s.bodyStart(text, delimiter)
	if bodyStart < 0 {
		// The command line has not all arrived.
		return "", text, false
	}
	body := text[bodyStart:]

	// The delimiter ends the body.
	//
	// It is on a line of its own — a newline, the delimiter, and a
	// newline — and its own newline may not have arrived yet. A body
	// with no newline in it at all leaves the delimiter against the last
	// line of it, so that is looked for too.
	//
	// The search is backwards, so the LAST delimiter in the body is the
	// one that ends it. A body cannot contain the delimiter: the
	// controller names it with an underscore in it, and an underscore is
	// not in the base64 alphabet, so a base64 body cannot hold one.
	//
	// Only the TAIL of the body is searched. The delimiter ends the
	// body, so it cannot be anywhere else in it — and the controller
	// sends a transfer line by line, so a body is megabytes and a
	// search of all of it on every line is quadratic in the size of the
	// transfer. A megabyte scanned 270,000 times is a transfer that
	// takes a minute to arrive.
	stop, end := findDelimiter(body, delimiter)
	if stop < 0 {
		// It has not arrived yet.
		return "", text, false
	}

	// The command is the heredoc, and the delimiter ends it.
	//
	// The line after the delimiter is NOT part of it. The controller
	// writes the mode on the command line — "base64 -d << 'DELIM' > PATH
	// && chmod MODE PATH" — and a controller that writes it after the
	// body instead writes it as a command of its own, which the plane
	// reads as one. Taking a line that is not there would swallow the
	// next command.
	// The command is the heredoc and the line that follows it.
	//
	// The controller writes the mode and the confirmation on the
	// command line — "base64 -d << 'DELIM' > PATH && chmod MODE PATH" —
	// and a controller that writes them after the body instead writes
	// them as a line of their own. Taking that line with the heredoc is
	// what stops it being read as a command that nothing recognises.
	at := bodyStart + end
	if nl := strings.Index(text[at:], "\n"); nl >= 0 {
		at += nl + 1
	} else if at < len(text) {
		// The line after the delimiter has not all arrived.
		return "", text, false
	}
	return text[:at], text[at:], true
}

// findDelimiter finds the delimiter at the end of a body.
//
// It reports where the delimiter starts and where the body ends after
// it. It reports -1 when the delimiter has not arrived.
//
// The whole body is searched while it is small enough that the search
// costs nothing, and only its tail when it is not — the delimiter ends
// the body, so it is at the end of it.
func findDelimiter(body, delimiter string) (stop, end int) {
	if len(body) <= fullScanLimit {
		stop = strings.LastIndex(body, "\n"+delimiter+"\n")
		if stop >= 0 {
			return stop, stop + len(delimiter) + 2
		}
		stop = strings.LastIndex(body, "\n"+delimiter)
		if stop >= 0 {
			return stop, stop + len(delimiter) + 1
		}
		stop = strings.LastIndex(body, delimiter)
		if stop >= 0 {
			return stop, stop + len(delimiter)
		}
		return -1, 0
	}

	// The tail is searched. It is a little longer than the delimiter so
	// that the newline before it is in the window too.
	window := len(delimiter) + 2
	if window > len(body) {
		window = len(body)
	}
	tail := body[len(body)-window:]
	if found := strings.LastIndex(tail, "\n"+delimiter+"\n"); found >= 0 {
		at := len(body) - window + found
		return at, at + len(delimiter) + 2
	}
	if found := strings.LastIndex(tail, "\n"+delimiter); found >= 0 {
		at := len(body) - window + found
		return at, at + len(delimiter) + 1
	}
	if found := strings.LastIndex(tail, delimiter); found >= 0 {
		at := len(body) - window + found
		return at, at + len(delimiter)
	}
	return -1, 0
}

// bodyStart is where the body of a heredoc begins.
//
// It is after the command line, which ends at the first newline after the
// delimiter was named. It reports -1 when the command line has not all
// arrived.
func (s *multiShellSession) bodyStart(text, delimiter string) int {
	idx := strings.Index(text, delimiter)
	if idx < 0 {
		return -1
	}
	// The delimiter is named twice: once on the command line and once at
	// the end of the body. The first is the one on the command line.
	after := idx + len(delimiter)
	nl := strings.Index(text[after:], "\n")
	if nl < 0 {
		return -1
	}
	return after + nl + 1
}

// buffered is a copy of what has not been acted on.
func (s *multiShellSession) buffered() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buffer.String()
}

// setBuffer keeps what has not been acted on.
func (s *multiShellSession) setBuffer(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buffer.Reset()
	s.buffer.WriteString(text)
}

// maxCompositeBytes is the largest command the composite will hold.
//
// The controller's largest is a base64 model transfer: 15 MB of model is
// about 20 MB of base64. The bound is above that, so a real transfer is
// never refused.
const maxCompositeBytes = 64 << 20

// fullScanLimit is the size above which the buffer is not searched whole.
//
// A transfer is megabytes and arrives line by line, so searching all of
// it on every line is quadratic in the size of the transfer. Above this
// size only the tail is searched, which is where a delimiter can be.
const fullScanLimit = 1 << 20

// Close ends every plane's session.
//
// The commands that have arrived are carried out first. The controller
// closes the session as soon as it has sent its last command, and a
// command dropped there is a credential that never lands.
func (s *multiShellSession) Close() error {
	var first error
	s.closeOnce.Do(func() {
		// The queue is drained before the planes are closed, so the
		// command the controller sent last is not lost to a close that
		// beat the worker to it.
		//
		// The drain is BOUNDED. The controller waits fifteen seconds
		// for the device to dial the next shell session, and the device
		// dials it only after this session has closed. A close that
		// waits for a command nothing is going to answer is fifteen
		// seconds the controller does not have.
		s.drain()

		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.queued.Broadcast()
		// The readers are woken, so a worker that is waiting for an
		// answer that will never come stops waiting. The controller
		// opens the next shell session straight away, and it waits for
		// the device to dial it — so a close that waits out an answer
		// timeout is a close that costs the controller its timeout.
		for _, reader := range s.readers {
			reader.wake()
		}
		s.worker.Wait()

		for _, plane := range s.planes {
			if err := plane.Close(); err != nil && first == nil {
				first = err
			}
		}
		s.mu.Lock()
		// The buffer is released. It holds the largest thing on the
		// wire, and the controller opens a session per command — so a
		// buffer that is not released is a buffer that is held for the
		// life of the process.
		s.buffer.Reset()
		s.out = nil
		s.mu.Unlock()
		s.ready.Broadcast()
	})
	return first
}

// drain waits for the commands that have arrived to be carried out.
//
// It gives up after a moment. A command that is still running when the
// session is closing is a command whose answer nobody will read, and the
// session must close: the controller opens the next one straight away,
// and it waits for the device to dial it.
func (s *multiShellSession) drain() {
	deadline := time.Now().Add(drainTimeout)
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.queue) > 0 || s.busy {
		if time.Now().After(deadline) {
			return
		}
		s.idle.Wait()
	}
}

// drainTimeout bounds how long a close waits for the command in flight.
//
// The controller opens a shell session per operation and waits fifteen
// seconds for the device to dial the next one. A close that waits longer
// than that spends the controller's whole timeout tidying up.
const drainTimeout = 2 * time.Second

// say records an answer and wakes a reader.
func (s *multiShellSession) say(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	s.out = append(s.out, text...)
	s.mu.Unlock()
	s.ready.Broadcast()
}

// run offers a command to every plane and takes the first answer.
//
// A plane that does not recognise the command refuses it, and the next
// one is asked. A plane that does recognise it answers, and the rest are
// not asked.
//
// A refusal is how a plane says "not mine", so it is not taken as the
// answer — otherwise the first plane would swallow every command and the
// others would never be asked.
func (s *multiShellSession) run(command string) string {
	for i, plane := range s.planes {
		answer, ok := askPlane(plane, s.reader(i), s.carries(i), command)
		if !ok || isRefusal(answer) {
			continue
		}
		return answer
	}
	return "echomuse: refused: no plane carries this command\n"
}

// reader is the reader for one plane.
func (s *multiShellSession) reader(index int) *planeReader {
	if index < 0 || index >= len(s.readers) {
		return nil
	}
	return s.readers[index]
}

// carries says whether a plane carries a large command.
func (s *multiShellSession) carries(index int) bool {
	if index < 0 || index >= len(s.carriers) {
		return false
	}
	return s.carriers[index]
}

// carriersOf says which of the runners carry a large command.
//
// A plane that says it will not is never asked about a transfer: a plane
// that will refuse it still has to HOLD it, and the transfer is the
// largest thing on the wire.
func carriersOf(runners []ShellRunner) []bool {
	carriers := make([]bool, 0, len(runners))
	for _, runner := range runners {
		carrier, ok := runner.(LargeCommandCarrier)
		carriers = append(carriers, ok && carrier.CarriesLargeCommands())
	}
	return carriers
}

// smallCommandBytes is the size above which a command is only offered to
// the planes that say they carry one.
//
// The controller's commands are small except for the file transfers,
// which are megabytes.
const smallCommandBytes = 64 << 10

// LargeCommandCarrier is a plane that can carry a large command.
//
// A plane that implements it is offered the controller's file transfers.
// A plane that does not is not asked about them at all, so it never
// holds a transfer it would only refuse.
type LargeCommandCarrier interface {
	// CarriesLargeCommands says the plane can carry one.
	CarriesLargeCommands() bool
}

// isRefusal reports whether an answer is a plane declining the command.
//
// A plane that does not recognise a command says so in words, and the
// words are the same across the SDK's planes.
func isRefusal(answer string) bool {
	lower := strings.ToLower(answer)
	return strings.Contains(lower, "refused") || strings.Contains(lower, "carries")
}

// askPlane offers a command to one plane.
//
// It reports whether the plane recognised it. A plane that answers with a
// refusal has recognised it — the command was for it, and it declined.
//
// The read is bounded: a plane that says nothing costs a moment and not
// the machine's memory.
func askPlane(plane ShellSession, reader *planeReader, carries bool, command string) (string, bool) {
	// A large command is offered only to the planes that carry one.
	//
	// A plane that will refuse it still has to HOLD it: the credential
	// plane copies every frame it is given into a buffer, so a
	// multi-megabyte transfer offered to it is copied in full and kept
	// for the length of the transfer. That is the largest thing on the
	// wire, and a plane that cannot use it should not be given it.
	if len(command) > smallCommandBytes && !carries {
		return "", false
	}
	if reader == nil {
		return "", false
	}
	// The command is written in one go. A plane is written one command
	// at a time, and a plane that is written twice for one command
	// answers twice — and the second answer is read as the answer to
	// the next command.
	if _, err := plane.Write([]byte(command)); err != nil {
		return "", false
	}

	timeout := planeAnswerTimeout
	if len(command) > smallCommandBytes {
		// A transfer is decoded and written to the disk before it is
		// answered, and that is not a moment's work.
		timeout = largePlaneAnswerTimeout
	}
	answer, ok := reader.next(timeout)
	if !ok {
		return "", false
	}
	if strings.TrimSpace(answer) == "" {
		// The plane said nothing. It did not recognise the command, and
		// the next plane is asked.
		return "", false
	}
	return answer, true
}

// planeAnswerTimeout bounds how long one plane is given to answer.
//
// The controller's shell commands are answered in a moment. A plane that
// takes longer is not answering this command, and the next plane is
// asked.
const planeAnswerTimeout = 2 * time.Second

// largePlaneAnswerTimeout bounds how long a plane is given to answer a
// file transfer.
//
// A transfer is decoded and written to the disk before it is answered,
// and a megabyte of base64 on a slow disk is not a moment's work. The
// bound is generous because it is only paid by a plane that does not
// answer at all — and such a plane holds the transfer for as long as the
// bound lasts, so it is not a bound to be casual about.
const largePlaneAnswerTimeout = 15 * time.Second

// answerQuiet is how long a plane is given to say more.
//
// It is what ends the answer of a plane that does not frame it.
const answerQuiet = 150 * time.Millisecond
