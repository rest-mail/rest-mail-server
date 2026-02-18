/* ============================================================
   REST Mail — Ethereal Landing Page Interactions
   "Because the mail server protocol is old, and needs to take a REST..."
   ============================================================ */

(function () {
  "use strict";

  // Mark that JS is running
  document.documentElement.classList.add("js-ready");

  // -------------------------------------------------------
  // Navigation scroll effect
  // -------------------------------------------------------
  var nav = document.getElementById("nav");

  function onScroll() {
    if (window.scrollY > 40) {
      nav.classList.add("scrolled");
    } else {
      nav.classList.remove("scrolled");
    }
  }

  window.addEventListener("scroll", onScroll, { passive: true });
  onScroll();

  // -------------------------------------------------------
  // Mobile nav toggle
  // -------------------------------------------------------
  var toggle = document.getElementById("nav-toggle");
  var links = document.getElementById("nav-links");

  if (toggle && links) {
    toggle.addEventListener("click", function () {
      links.classList.toggle("open");
      var spans = toggle.querySelectorAll("span");
      if (links.classList.contains("open")) {
        spans[0].style.transform = "rotate(45deg) translate(5px, 5px)";
        spans[1].style.opacity = "0";
        spans[2].style.transform = "rotate(-45deg) translate(5px, -5px)";
      } else {
        spans[0].style.transform = "";
        spans[1].style.opacity = "";
        spans[2].style.transform = "";
      }
    });

    links.querySelectorAll("a").forEach(function (a) {
      a.addEventListener("click", function () {
        links.classList.remove("open");
        var spans = toggle.querySelectorAll("span");
        spans[0].style.transform = "";
        spans[1].style.opacity = "";
        spans[2].style.transform = "";
      });
    });
  }

  // -------------------------------------------------------
  // Scroll-triggered visibility (IntersectionObserver)
  // -------------------------------------------------------
  var observerOptions = {
    root: null,
    rootMargin: "0px 0px -60px 0px",
    threshold: 0.1,
  };

  var observer = new IntersectionObserver(function (entries) {
    entries.forEach(function (entry) {
      if (entry.isIntersecting) {
        entry.target.classList.add("visible");
        observer.unobserve(entry.target);
      }
    });
  }, observerOptions);

  var selectors = [
    ".feature-card",
    ".stack-card",
    ".flow-card",
    ".domain-card",
    ".protocol-step",
    ".what-problem",
    ".what-solution",
  ];

  selectors.forEach(function (sel) {
    document.querySelectorAll(sel).forEach(function (el) {
      observer.observe(el);
    });
  });

  // -------------------------------------------------------
  // Smooth scroll for anchor links
  // -------------------------------------------------------
  document.querySelectorAll('a[href^="#"]').forEach(function (anchor) {
    anchor.addEventListener("click", function (e) {
      var href = this.getAttribute("href");
      if (href === "#") return;
      var target = document.querySelector(href);
      if (target) {
        e.preventDefault();
        var offsetTop = target.getBoundingClientRect().top + window.scrollY - 80;
        window.scrollTo({ top: offsetTop, behavior: "smooth" });
      }
    });
  });

  // -------------------------------------------------------
  // Ascending protocol ghosts — gentle floating text
  // -------------------------------------------------------
  var protocolContainer = document.getElementById("ascending-protocols");
  var protocols = ["SMTP", "IMAP", "POP3", "RFC 821", "RFC 3501", "POP3S", "SMTP/TLS", "EHLO", "STARTTLS", "MAIL FROM", "RCPT TO", "QUIT"];

  function createGhost() {
    if (!protocolContainer) return;

    var ghost = document.createElement("div");
    ghost.className = "ascending-ghost";
    ghost.textContent = protocols[Math.floor(Math.random() * protocols.length)];

    // Random horizontal position
    ghost.style.left = (Math.random() * 90 + 5) + "%";

    // Random size
    var size = 0.75 + Math.random() * 1.0;
    ghost.style.fontSize = size + "rem";

    // Random duration (slow and gentle)
    var duration = 20 + Math.random() * 30;
    ghost.style.animationDuration = duration + "s";

    // Random delay so they don't all start at once
    ghost.style.animationDelay = -(Math.random() * duration) + "s";

    // Random opacity range
    ghost.style.opacity = "0";

    protocolContainer.appendChild(ghost);

    // Clean up after animation completes
    setTimeout(function () {
      if (ghost.parentNode) {
        ghost.parentNode.removeChild(ghost);
      }
    }, duration * 1000 + 1000);
  }

  // Create initial batch
  for (var i = 0; i < 8; i++) {
    createGhost();
  }

  // Continuously spawn new ghosts
  setInterval(function () {
    if (document.querySelectorAll(".ascending-ghost").length < 12) {
      createGhost();
    }
  }, 4000);

  // -------------------------------------------------------
  // Subtle parallax on scroll
  // -------------------------------------------------------
  var heroContent = document.querySelector(".hero-content");
  var heroClouds = document.querySelector(".hero-clouds");
  var heroSunbeam = document.querySelector(".hero-sunbeam");

  function parallax() {
    var scrolled = window.scrollY;
    var viewHeight = window.innerHeight;

    // Only apply parallax within the hero area
    if (scrolled < viewHeight * 1.5) {
      if (heroContent) {
        heroContent.style.transform = "translateY(" + (scrolled * 0.15) + "px)";
        heroContent.style.opacity = Math.max(0, 1 - scrolled / (viewHeight * 0.8));
      }
      if (heroClouds) {
        heroClouds.style.transform = "translateY(" + (scrolled * 0.08) + "px)";
      }
      if (heroSunbeam) {
        heroSunbeam.style.transform = "translateX(-50%) translateY(" + (scrolled * -0.05) + "px)";
        heroSunbeam.style.opacity = Math.max(0, 1 - scrolled / (viewHeight * 0.6));
      }
    }
  }

  // Use requestAnimationFrame for smooth parallax
  var ticking = false;
  window.addEventListener("scroll", function () {
    if (!ticking) {
      window.requestAnimationFrame(function () {
        parallax();
        ticking = false;
      });
      ticking = true;
    }
  }, { passive: true });

  // -------------------------------------------------------
  // Protocol ghost hover effect in hero
  // -------------------------------------------------------
  document.querySelectorAll(".protocol-ghost").forEach(function (ghost) {
    ghost.addEventListener("mouseenter", function () {
      this.style.opacity = "0.9";
      this.style.color = "var(--text-0)";
      this.style.transition = "all 0.3s ease";
    });

    ghost.addEventListener("mouseleave", function () {
      this.style.opacity = "";
      this.style.color = "";
    });
  });

})();
