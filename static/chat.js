// Keyboard handling and scroll behaviour for the chat page. Every other
// interaction on the page uses HTMX attributes.
(function () {
	"use strict";

	var box = document.getElementById("message");
	var form = document.getElementById("msgform");
	var pane = document.getElementById("lines");
	if (!box || !form || !pane) { return; }

	// Enter sends the message. Shift and Enter make a new line, which is
	// the default action of the textarea. The isComposing test prevents a
	// send when an input method accepts a candidate word, which matters
	// for Japanese, Chinese, and Korean input.
	box.addEventListener("keydown", function (evt) {
		if (evt.key !== "Enter" || evt.shiftKey || evt.isComposing) { return; }
		evt.preventDefault();
		if (box.value.trim() !== "") { form.requestSubmit(); }
	});

	// The box grows with the text to a limit of six lines.
	function resize() {
		box.style.height = "auto";
		box.style.height = Math.min(box.scrollHeight, 144) + "px";
	}
	box.addEventListener("input", resize);

	// Clear the box after a successful send and keep the focus.
	document.body.addEventListener("htmx:afterRequest", function (evt) {
		if (evt.detail.elt !== form || !evt.detail.successful) { return; }
		box.value = "";
		resize();
		box.focus();
	});

	// Follow the newest message, but only when the reader is already at the
	// end. A person that scrolled up to read old messages stays in place.
	var atEnd = true;
	pane.addEventListener("scroll", function () {
		atEnd = pane.scrollHeight - pane.scrollTop - pane.clientHeight < 40;
	});
	document.body.addEventListener("htmx:afterSwap", function () {
		if (atEnd) { pane.scrollTop = pane.scrollHeight; }
	});

	pane.scrollTop = pane.scrollHeight;
	box.focus();
})();

